// Package torchpt reads PyTorch checkpoints (torch.save's zip format:
// archive/data.pkl + archive/data/<storage>) without Python and without
// executing anything: a restricted pickle machine that knows how tensors
// are rebuilt and treats every other class as an opaque object. It
// converts state dicts to safetensors, which MLX loads, folding weight
// normalisation (weight_g, weight_v) into plain weights.
//
// The engine's AudioSeal watermarker (facebook/audioseal ships .pth files
// only) is its user: the app converts the snapshot once, next to the
// model cache, and the engine loads the safetensors.
package torchpt

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Tensor is a float32 tensor of a checkpoint.
type Tensor struct {
	Shape []int
	Data  []float32
}

// Checkpoint is what a torch.save file holds: its top-level object, and
// the state dicts (string → tensor) found in it by key path ("model",
// "state_dict", or "" for a top-level state dict).
type Checkpoint struct {
	Root   any
	tensor map[*tensorRef]Tensor
}

// Open reads a checkpoint.
func Open(path string) (*Checkpoint, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%s: not a torch zip checkpoint: %w", filepath.Base(path), err)
	}
	defer z.Close()
	var pkl *zip.File
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
		if strings.HasSuffix(f.Name, "/data.pkl") || f.Name == "data.pkl" {
			pkl = f
		}
	}
	if pkl == nil {
		return nil, errors.New("no data.pkl in the checkpoint")
	}
	prefix := strings.TrimSuffix(pkl.Name, "data.pkl")
	if bo, ok := files[prefix+"byteorder"]; ok {
		if b, err := readFile(bo); err != nil || strings.TrimSpace(string(b)) != "little" {
			return nil, fmt.Errorf("byte order %q: only little-endian checkpoints", b)
		}
	}
	data, err := readFile(pkl)
	if err != nil {
		return nil, err
	}
	m := &machine{r: bytes.NewReader(data), memo: map[int]any{}}
	root, err := m.run()
	if err != nil {
		return nil, fmt.Errorf("data.pkl: %w", err)
	}
	ck := &Checkpoint{Root: root, tensor: map[*tensorRef]Tensor{}}
	storages := map[string][]float32{}
	for _, t := range m.tensors {
		st, ok := storages[t.storage.key]
		if !ok {
			f := files[prefix+"data/"+t.storage.key]
			if f == nil {
				return nil, fmt.Errorf("storage %s is missing", t.storage.key)
			}
			b, err := readFile(f)
			if err != nil {
				return nil, err
			}
			if t.storage.dtype != "FloatStorage" {
				return nil, fmt.Errorf("storage %s: %s (only float32)", t.storage.key, t.storage.dtype)
			}
			st = make([]float32, len(b)/4)
			for i := range st {
				st[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
			}
			storages[t.storage.key] = st
		}
		n := 1
		for _, d := range t.shape {
			n *= d
		}
		// Contiguous (row-major) only, as state dicts are.
		want := 1
		for i := len(t.shape) - 1; i >= 0; i-- {
			if t.shape[i] != 1 && t.stride[i] != want {
				return nil, fmt.Errorf("tensor in storage %s is not contiguous (shape %v, stride %v)", t.storage.key, t.shape, t.stride)
			}
			want *= t.shape[i]
		}
		if t.offset+n > len(st) {
			return nil, fmt.Errorf("tensor in storage %s overruns it", t.storage.key)
		}
		ck.tensor[t] = Tensor{Shape: slices.Clone(t.shape), Data: st[t.offset : t.offset+n]}
	}
	return ck, nil
}

func readFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// StateDict returns the tensors of the dict at key in the root ("" = the
// root itself), by name; values that are not tensors are skipped.
func (c *Checkpoint) StateDict(key string) (map[string]Tensor, error) {
	d, ok := c.Root.(*Dict)
	if !ok {
		return nil, errors.New("the checkpoint is not a dict")
	}
	if key != "" {
		v, ok := d.Get(key)
		if !ok {
			return nil, fmt.Errorf("no %q in the checkpoint (keys %v)", key, d.Keys())
		}
		if d, ok = v.(*Dict); !ok {
			return nil, fmt.Errorf("%q is not a dict", key)
		}
	}
	out := map[string]Tensor{}
	for i, k := range d.keys {
		name, ok := k.(string)
		if !ok {
			continue
		}
		if t, ok := d.vals[i].(*tensorRef); ok {
			out[name] = c.tensor[t]
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tensors under %q", key)
	}
	return out, nil
}

// FoldWeightNorm replaces every <p>.weight_g / <p>.weight_v pair (torch's
// weight_norm, dim 0) by <p>.weight = g · v / ‖v‖, the norm taken over all
// dimensions but the first.
func FoldWeightNorm(sd map[string]Tensor) error {
	for name, g := range sd {
		p, ok := strings.CutSuffix(name, ".weight_g")
		if !ok {
			continue
		}
		v, ok := sd[p+".weight_v"]
		if !ok {
			return fmt.Errorf("%s has no weight_v", p)
		}
		rows := v.Shape[0]
		if len(g.Data) != rows {
			return fmt.Errorf("%s: weight_g has %d values for %d rows", p, len(g.Data), rows)
		}
		per := len(v.Data) / rows
		w := make([]float32, len(v.Data))
		for r := range rows {
			var ss float64
			for _, x := range v.Data[r*per : (r+1)*per] {
				ss += float64(x) * float64(x)
			}
			scale := float64(g.Data[r]) / math.Sqrt(ss)
			for i, x := range v.Data[r*per : (r+1)*per] {
				w[r*per+i] = float32(float64(x) * scale)
			}
		}
		sd[p+".weight"] = Tensor{Shape: v.Shape, Data: w}
		delete(sd, name)
		delete(sd, p+".weight_v")
	}
	return nil
}

// WriteSafetensors writes tensors as a float32 safetensors file, sorted by
// name, through a temporary file renamed into place.
func WriteSafetensors(path string, sd map[string]Tensor, metadata map[string]string) error {
	names := make([]string, 0, len(sd))
	for n := range sd {
		names = append(names, n)
	}
	sort.Strings(names)
	header := map[string]any{}
	if len(metadata) > 0 {
		header["__metadata__"] = metadata
	}
	off := 0
	for _, n := range names {
		t := sd[n]
		header[n] = map[string]any{"dtype": "F32", "shape": t.Shape, "data_offsets": []int{off, off + 4*len(t.Data)}}
		off += 4 * len(t.Data)
	}
	hj, err := json.Marshal(header)
	if err != nil {
		return err
	}
	for len(hj)%8 != 0 {
		hj = append(hj, ' ')
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.safetensors")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := &errWriter{w: tmp}
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(hj)))
	w.write(n[:])
	w.write(hj)
	buf := make([]byte, 0, 1<<16)
	for _, name := range names {
		for _, x := range sd[name].Data {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(x))
			if len(buf) >= 1<<16 {
				w.write(buf)
				buf = buf[:0]
			}
		}
	}
	w.write(buf)
	if w.err == nil {
		w.err = tmp.Close()
	} else {
		tmp.Close()
	}
	if w.err != nil {
		return w.err
	}
	return os.Rename(tmp.Name(), path)
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) write(b []byte) {
	if e.err == nil {
		_, e.err = e.w.Write(b)
	}
}

// ConvertFile converts the state dict at key of a checkpoint to a
// safetensors file, folding weight normalisation.
func ConvertFile(src, key, dst string, metadata map[string]string) error {
	ck, err := Open(src)
	if err != nil {
		return err
	}
	sd, err := ck.StateDict(key)
	if err != nil {
		return err
	}
	if err := FoldWeightNorm(sd); err != nil {
		return err
	}
	return WriteSafetensors(dst, sd, metadata)
}

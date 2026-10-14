package torchpt

import (
	"archive/zip"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pickleOps assembles a pickle the way torch.save writes a state dict:
// {"cfg": <object>, "model": OrderedDict{"conv.weight_g", "conv.weight_v",
// "conv.bias"}}, with weight_v and bias sharing storage "1".
func writeCheckpoint(t *testing.T, path string) {
	t.Helper()
	var p []byte
	op := func(b ...byte) { p = append(p, b...) }
	str := func(s string) {
		op('X')
		p = binary.LittleEndian.AppendUint32(p, uint32(len(s)))
		p = append(p, s...)
	}
	global := func(mod, name string) { op('c'); p = append(p, mod+"\n"+name+"\n"...) }
	tensor := func(key string, numel, off int, shape ...int) {
		global("torch._utils", "_rebuild_tensor_v2")
		op('(')
		// persistent id ('storage', FloatStorage, key, 'cpu', numel)
		op('(')
		str("storage")
		global("torch", "FloatStorage")
		str(key)
		str("cpu")
		op('K', byte(numel))
		op('t', 'Q')
		op('K', byte(off))
		op('(')
		for _, d := range shape {
			op('K', byte(d))
		}
		op('t', '(')
		stride := 1
		strides := make([]int, len(shape))
		for i := len(shape) - 1; i >= 0; i-- {
			strides[i] = stride
			stride *= shape[i]
		}
		for _, s := range strides {
			op('K', byte(s))
		}
		op('t', 0x89, ')', 't', 'R')
	}
	op(0x80, 2, '}', 'q', 0, '(')
	str("cfg")
	global("omegaconf.dictconfig", "DictConfig")
	op(')', 0x81, '}', '(')
	str("_content")
	str("seanet")
	op('u', 'b')
	str("model")
	global("collections", "OrderedDict")
	op(')', 'R', 'q', 1, '(')
	str("conv.weight_g")
	tensor("0", 2, 0, 2, 1, 1)
	str("conv.weight_v")
	tensor("1", 8, 0, 2, 1, 3)
	str("conv.bias")
	tensor("1", 8, 6, 2)
	op('u', 'u', '.')
	f, _ := os.Create(path)
	z := zip.NewWriter(f)
	put := func(name string, b []byte) {
		w, _ := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		w.Write(b)
	}
	floats := func(xs ...float32) []byte {
		var b []byte
		for _, x := range xs {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
		}
		return b
	}
	put("ck/data.pkl", p)
	put("ck/byteorder", []byte("little"))
	put("ck/data/0", floats(2, 1))
	put("ck/data/1", floats(3, 4, 0, 0, 0, 5, 0.5, -0.5))
	z.Close()
	f.Close()
}

func TestConvert(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "ck.pth")
	writeCheckpoint(t, src)
	ck, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ck.StateDict("nope"); err == nil {
		t.Error("missing key")
	}
	dst := filepath.Join(dir, "ck.safetensors")
	if err := ConvertFile(src, "model", dst, map[string]string{"source": "test"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	n := binary.LittleEndian.Uint64(b)
	var h map[string]json.RawMessage
	if err := json.Unmarshal(b[8:8+n], &h); err != nil {
		t.Fatal(err)
	}
	if _, ok := h["conv.weight_g"]; ok || h["conv.weight"] == nil || h["conv.bias"] == nil || !strings.Contains(string(h["__metadata__"]), "test") {
		t.Fatalf("header %s", b[8:8+n])
	}
	data := b[8+n:]
	f := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:])) }
	// Sorted: conv.bias (2), conv.weight (6). weight row 0 = 2·(3,4,0)/5, row 1 = 1·(0,0,5)/5.
	got := []float32{f(0), f(1), f(2), f(3), f(4), f(5), f(6), f(7)}
	want := []float32{0.5, -0.5, 1.2, 1.6, 0, 0, 0, 1}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			t.Fatalf("data %v, want %v", got, want)
		}
	}
}

// The real AudioSeal checkpoints (AUDIOSMITH_AUDIOSEAL_DIR: a folder with
// generator_base.pth and detector_base.pth).
func TestAudioSealCheckpoints(t *testing.T) {
	dir := os.Getenv("AUDIOSMITH_AUDIOSEAL_DIR")
	if dir == "" {
		t.Skip("set AUDIOSMITH_AUDIOSEAL_DIR")
	}
	for name, want := range map[string]int{"generator_base.pth": 101, "detector_base.pth": 54} {
		ck, err := Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sd, err := ck.StateDict("model")
		if err != nil {
			t.Fatal(err)
		}
		if len(sd) != want {
			t.Errorf("%s: %d tensors", name, len(sd))
		}
		if err := FoldWeightNorm(sd); err != nil {
			t.Fatal(err)
		}
		for k, v := range sd {
			n := 1
			for _, d := range v.Shape {
				n *= d
			}
			if n != len(v.Data) || strings.HasSuffix(k, "_g") {
				t.Errorf("%s %v %d", k, v.Shape, len(v.Data))
			}
		}
		t.Logf("%s: %d tensors after folding", name, len(sd))
	}
}

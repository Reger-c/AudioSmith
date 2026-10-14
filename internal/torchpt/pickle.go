package torchpt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
)

// A restricted pickle machine (protocols 2–5). It never imports or calls
// anything: globals are names; calling one makes a tensor reference
// (torch._utils._rebuild_tensor_v2 / _rebuild_parameter), an ordered dict
// (collections.OrderedDict, builtins dict) or an opaque Object.

// Dict is an insertion-ordered dict.
type Dict struct {
	keys, vals []any
}

// Get returns the value of a string key.
func (d *Dict) Get(key string) (any, bool) {
	for i, k := range d.keys {
		if s, ok := k.(string); ok && s == key {
			return d.vals[i], true
		}
	}
	return nil, false
}

// Keys returns the string keys.
func (d *Dict) Keys() []string {
	var out []string
	for _, k := range d.keys {
		if s, ok := k.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (d *Dict) set(k, v any) {
	for i, x := range d.keys {
		if x == k {
			d.vals[i] = v
			return
		}
	}
	d.keys = append(d.keys, k)
	d.vals = append(d.vals, v)
}

// Global is a class or function named by the pickle.
type Global struct{ Module, Name string }

// Object is an instance of a class the machine does not know (omegaconf
// configs…): its class, constructor arguments, state, items and list.
type Object struct {
	Class Global
	Args  []any
	State any
	Items *Dict
	List  []any
}

type tuple []any

type list struct{ items []any }

type storageRef struct {
	dtype string // "FloatStorage"…
	key   string
	numel int
}

type tensorRef struct {
	storage storageRef
	offset  int
	shape   []int
	stride  []int
}

type mark struct{}

type machine struct {
	r       *bytes.Reader
	stack   []any
	memo    map[int]any
	tensors []*tensorRef
}

func (m *machine) push(v any) { m.stack = append(m.stack, v) }

func (m *machine) pop() (any, error) {
	if len(m.stack) == 0 {
		return nil, errors.New("stack underflow")
	}
	v := m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]
	return v, nil
}

func (m *machine) top() (any, error) {
	if len(m.stack) == 0 {
		return nil, errors.New("stack underflow")
	}
	return m.stack[len(m.stack)-1], nil
}

// popMark pops everything above the last mark.
func (m *machine) popMark() ([]any, error) {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if _, ok := m.stack[i].(mark); ok {
			items := append([]any(nil), m.stack[i+1:]...)
			m.stack = m.stack[:i]
			return items, nil
		}
	}
	return nil, errors.New("no mark")
}

func (m *machine) n(k int) ([]byte, error) {
	b := make([]byte, k)
	_, err := io.ReadFull(m.r, b)
	return b, err
}

func (m *machine) u8() (int, error) {
	b, err := m.r.ReadByte()
	return int(b), err
}

func (m *machine) u16() (int, error) {
	b, err := m.n(2)
	if err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint16(b)), nil
}

func (m *machine) u32() (int, error) {
	b, err := m.n(4)
	if err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint32(b)), nil
}

func (m *machine) line() (string, error) {
	var b []byte
	for {
		c, err := m.r.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '\n' {
			return string(b), nil
		}
		b = append(b, c)
	}
}

func (m *machine) run() (any, error) {
	for {
		op, err := m.r.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("truncated: %w", err)
		}
		if err := m.step(op); err != nil {
			if errors.Is(err, errStop) {
				return m.pop()
			}
			return nil, fmt.Errorf("opcode 0x%02x at %d: %w", op, int(m.r.Size())-m.r.Len()-1, err)
		}
	}
}

var errStop = errors.New("stop")

func (m *machine) step(op byte) error {
	switch op {
	case 0x80: // PROTO
		_, err := m.u8()
		return err
	case 0x95: // FRAME
		_, err := m.n(8)
		return err
	case '.': // STOP
		return errStop
	case '(': // MARK
		m.push(mark{})
	case '}': // EMPTY_DICT
		m.push(&Dict{})
	case ']': // EMPTY_LIST
		m.push(&list{})
	case ')': // EMPTY_TUPLE
		m.push(tuple{})
	case 'N':
		m.push(nil)
	case 0x88: // NEWTRUE
		m.push(true)
	case 0x89: // NEWFALSE
		m.push(false)
	case 'J': // BININT
		v, err := m.u32()
		m.push(int(int32(uint32(v))))
		return err
	case 'K': // BININT1
		v, err := m.u8()
		m.push(v)
		return err
	case 'M': // BININT2
		v, err := m.u16()
		m.push(v)
		return err
	case 0x8a: // LONG1
		n, err := m.u8()
		if err != nil {
			return err
		}
		b, err := m.n(n)
		if err != nil {
			return err
		}
		m.push(longLE(b))
	case 'G': // BINFLOAT (big-endian)
		b, err := m.n(8)
		m.push(math.Float64frombits(binary.BigEndian.Uint64(b)))
		return err
	case 'X': // BINUNICODE
		n, err := m.u32()
		if err != nil {
			return err
		}
		b, err := m.n(n)
		m.push(string(b))
		return err
	case 0x8c: // SHORT_BINUNICODE
		n, err := m.u8()
		if err != nil {
			return err
		}
		b, err := m.n(n)
		m.push(string(b))
		return err
	case 'C': // SHORT_BINBYTES
		n, err := m.u8()
		if err != nil {
			return err
		}
		b, err := m.n(n)
		m.push(b)
		return err
	case 'B': // BINBYTES
		n, err := m.u32()
		if err != nil {
			return err
		}
		b, err := m.n(n)
		m.push(b)
		return err
	case 'q': // BINPUT
		i, err := m.u8()
		if err != nil {
			return err
		}
		v, err := m.top()
		m.memo[i] = v
		return err
	case 'r': // LONG_BINPUT
		i, err := m.u32()
		if err != nil {
			return err
		}
		v, err := m.top()
		m.memo[i] = v
		return err
	case 0x94: // MEMOIZE
		v, err := m.top()
		m.memo[len(m.memo)] = v
		return err
	case 'h': // BINGET
		i, err := m.u8()
		if err != nil {
			return err
		}
		v, ok := m.memo[i]
		if !ok {
			return fmt.Errorf("memo %d unset", i)
		}
		m.push(v)
	case 'j': // LONG_BINGET
		i, err := m.u32()
		if err != nil {
			return err
		}
		v, ok := m.memo[i]
		if !ok {
			return fmt.Errorf("memo %d unset", i)
		}
		m.push(v)
	case 't': // TUPLE
		items, err := m.popMark()
		m.push(tuple(items))
		return err
	case 0x85, 0x86, 0x87: // TUPLE1..3
		n := int(op - 0x84)
		if len(m.stack) < n {
			return errors.New("stack underflow")
		}
		t := tuple(append([]any(nil), m.stack[len(m.stack)-n:]...))
		m.stack = m.stack[:len(m.stack)-n]
		m.push(t)
	case 'a': // APPEND
		v, err := m.pop()
		if err != nil {
			return err
		}
		return m.appendTo([]any{v})
	case 'e': // APPENDS
		items, err := m.popMark()
		if err != nil {
			return err
		}
		return m.appendTo(items)
	case 's': // SETITEM
		v, err := m.pop()
		if err != nil {
			return err
		}
		k, err := m.pop()
		if err != nil {
			return err
		}
		return m.setItems([]any{k, v})
	case 'u': // SETITEMS
		items, err := m.popMark()
		if err != nil {
			return err
		}
		return m.setItems(items)
	case 'c': // GLOBAL
		mod, err := m.line()
		if err != nil {
			return err
		}
		name, err := m.line()
		m.push(Global{mod, name})
		return err
	case 0x93: // STACK_GLOBAL
		name, err := m.pop()
		if err != nil {
			return err
		}
		mod, err := m.pop()
		if err != nil {
			return err
		}
		ms, ok1 := mod.(string)
		ns, ok2 := name.(string)
		if !ok1 || !ok2 {
			return errors.New("STACK_GLOBAL of non-strings")
		}
		m.push(Global{ms, ns})
	case 'Q': // BINPERSID
		pid, err := m.pop()
		if err != nil {
			return err
		}
		t, ok := pid.(tuple)
		if !ok || len(t) < 5 || t[0] != "storage" {
			return fmt.Errorf("persistent id %v", pid)
		}
		g, _ := t[1].(Global)
		key, _ := t[2].(string)
		numel, _ := t[4].(int)
		m.push(storageRef{dtype: g.Name, key: key, numel: numel})
	case 'R': // REDUCE
		args, err := m.pop()
		if err != nil {
			return err
		}
		fn, err := m.pop()
		if err != nil {
			return err
		}
		v, err := m.call(fn, args)
		m.push(v)
		return err
	case 0x81: // NEWOBJ
		args, err := m.pop()
		if err != nil {
			return err
		}
		cls, err := m.pop()
		if err != nil {
			return err
		}
		g, _ := cls.(Global)
		at, _ := args.(tuple)
		if g == (Global{"collections", "OrderedDict"}) || g == (Global{"builtins", "dict"}) || g == (Global{"__builtin__", "dict"}) {
			m.push(&Dict{})
			return nil
		}
		m.push(&Object{Class: g, Args: at})
	case 'b': // BUILD
		state, err := m.pop()
		if err != nil {
			return err
		}
		v, err := m.top()
		if err != nil {
			return err
		}
		if o, ok := v.(*Object); ok {
			o.State = state
		}
	default:
		return errors.New("unsupported opcode")
	}
	return nil
}

func longLE(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	be := make([]byte, len(b))
	for i := range b {
		be[len(b)-1-i] = b[i]
	}
	v := new(big.Int).SetBytes(be)
	if b[len(b)-1]&0x80 != 0 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b))))
	}
	return int(v.Int64())
}

func (m *machine) appendTo(items []any) error {
	v, err := m.top()
	if err != nil {
		return err
	}
	switch l := v.(type) {
	case *list:
		l.items = append(l.items, items...)
	case *Object:
		l.List = append(l.List, items...)
	default:
		return fmt.Errorf("append to %T", v)
	}
	return nil
}

func (m *machine) setItems(kv []any) error {
	if len(kv)%2 != 0 {
		return errors.New("odd SETITEMS")
	}
	v, err := m.top()
	if err != nil {
		return err
	}
	var d *Dict
	switch x := v.(type) {
	case *Dict:
		d = x
	case *Object:
		if x.Items == nil {
			x.Items = &Dict{}
		}
		d = x.Items
	default:
		return fmt.Errorf("set items of %T", v)
	}
	for i := 0; i < len(kv); i += 2 {
		k := kv[i]
		switch k.(type) {
		case string, int, nil, bool, float64:
		default:
			k = fmt.Sprintf("%p", k) // unhashable keys keep their order only
		}
		d.set(k, kv[i+1])
	}
	return nil
}

func ints(v any) ([]int, bool) {
	t, ok := v.(tuple)
	if !ok {
		return nil, false
	}
	out := make([]int, len(t))
	for i, x := range t {
		n, ok := x.(int)
		if !ok {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

func (m *machine) call(fn, args any) (any, error) {
	g, ok := fn.(Global)
	a, _ := args.(tuple)
	if !ok {
		return &Object{Args: a}, nil
	}
	switch g {
	case Global{"torch._utils", "_rebuild_tensor_v2"}, Global{"torch._utils", "_rebuild_tensor"}:
		if len(a) < 4 {
			return nil, errors.New("_rebuild_tensor: too few arguments")
		}
		st, ok := a[0].(storageRef)
		off, ok2 := a[1].(int)
		shape, ok3 := ints(a[2])
		stride, ok4 := ints(a[3])
		if !ok || !ok2 || !ok3 || !ok4 || len(shape) != len(stride) {
			return nil, fmt.Errorf("_rebuild_tensor arguments %v", a[:4])
		}
		t := &tensorRef{storage: st, offset: off, shape: shape, stride: stride}
		m.tensors = append(m.tensors, t)
		return t, nil
	case Global{"torch._utils", "_rebuild_parameter"}:
		if len(a) > 0 {
			if t, ok := a[0].(*tensorRef); ok {
				return t, nil
			}
		}
		return nil, errors.New("_rebuild_parameter of a non-tensor")
	case Global{"collections", "OrderedDict"}, Global{"builtins", "dict"}, Global{"__builtin__", "dict"}:
		return &Dict{}, nil
	}
	return &Object{Class: g, Args: a}, nil
}

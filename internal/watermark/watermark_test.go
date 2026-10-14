package watermark

import "testing"

func TestMessage(t *testing.T) {
	m := Message(0x3C)
	if m != 0xA53C {
		t.Fatalf("%x", m)
	}
	if ours, id := Decode(m); !ours || id != 0x3C {
		t.Fatal(ours, id)
	}
	if ours, _ := Decode(0x1234); ours {
		t.Fatal("not ours")
	}
}

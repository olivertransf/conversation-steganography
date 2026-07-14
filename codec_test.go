package decalgo

import (
	"strings"
	"testing"
)

func TestStreamingRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	e, _ := NewEncoder(key, "chat-42")
	d, _ := NewDecoder(key, "chat-42")
	for _, want := range []string{"first phrase", "continues here", "and finishes"} {
		wire, err := e.Encode(want)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(wire, "enc:DEC1.") {
			t.Fatalf("missing explicit marker: %q", wire)
		}
		got, err := d.Decode(wire)
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
}

func TestUnmarkedInputIsRejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	d, _ := NewDecoder(key, "chat-42")
	if _, err := d.Decode("DEC1.abc"); err == nil {
		t.Fatal("unmarked input accepted")
	}
}

func TestWrongKeyAndOrderFail(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	e, _ := NewEncoder(key, "chat-42")
	first, _ := e.Encode("first")
	second, _ := e.Encode("second")

	wrong, _ := NewDecoder([]byte("abcdef0123456789abcdef0123456789"), "chat-42")
	if _, err := wrong.Decode(first); err == nil {
		t.Fatal("wrong key accepted")
	}

	d, _ := NewDecoder(key, "chat-42")
	if _, err := d.Decode(second); err == nil {
		t.Fatal("out-of-order message accepted")
	}
}

func TestTamperingFails(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	e, _ := NewEncoder(key, "chat-42")
	d, _ := NewDecoder(key, "chat-42")
	wire, _ := e.Encode("secret")
	wire = wire[:len(wire)-1] + "A"
	if _, err := d.Decode(wire); err == nil {
		t.Fatal("tampered message accepted")
	}
}

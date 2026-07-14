package decalgo

import (
	"bytes"
	"testing"
)

func TestChatMessagePackingRoundTrip(t *testing.T) {
	for _, original := range [][]byte{
		{},
		[]byte("i think i might've fucked up a bit too hard"),
		{0, 1, 2, 3, 254, 255},
	} {
		packed, err := packMessage(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := unpackMessage(packed)
		if err != nil || !bytes.Equal(decoded, original) {
			t.Fatalf("round trip %q: %q, %v", original, decoded, err)
		}
	}
}

func TestCommonChatMessageCompresses(t *testing.T) {
	original := []byte("i think i might've fucked up a bit too hard")
	packed, err := packMessage(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) >= len(original)/2 {
		t.Fatalf("packed message is still too large: %d bytes from %d", len(packed), len(original))
	}
}

package decalgo

import "testing"

func TestSecureCarrierRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	sealed, err := SealCarrierPayload(key, "chat-1", "alice-to-bob", 7, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenCarrierPayload(key, "chat-1", "alice-to-bob", 7, sealed)
	if err != nil || string(got) != "hello" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := OpenCarrierPayload(key, "chat-1", "alice-to-bob", 8, sealed); err == nil {
		t.Fatal("wrong sequence accepted")
	}
	if _, err := OpenCarrierPayload(key, "chat-1", "bob-to-alice", 7, sealed); err == nil {
		t.Fatal("wrong direction accepted")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := OpenCarrierPayload(key, "chat-1", "alice-to-bob", 7, sealed); err == nil {
		t.Fatal("tampering accepted")
	}
}

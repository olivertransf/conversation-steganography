package decalgo

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestAESCMACKnownVector(t *testing.T) {
	key, _ := hex.DecodeString("2b7e151628aed2a6abf7158809cf4f3c")
	want, _ := hex.DecodeString("bb1d6929e95937287fa37d129b756746")
	got, err := aesCMAC(key, nil)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("CMAC = %x, %v; want %x", got, err, want)
	}
}

func TestS2VRFC5297Vector(t *testing.T) {
	macKey, _ := hex.DecodeString("fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0")
	aad, _ := hex.DecodeString("101112131415161718191a1b1c1d1e1f2021222324252627")
	plaintext, _ := hex.DecodeString("112233445566778899aabbccddee")
	want, _ := hex.DecodeString("85632d07c6e8f37f950acd320a2ecc93")
	got, err := s2v(macKey, aad, plaintext)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("S2V = %x, %v; want %x", got, err, want)
	}
}

func TestSIVRoundTripDeterminismAndAuthentication(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	aad := []byte("ordered conversation state")
	plaintext := []byte("exact private message")
	first, err := sealSIV(key, aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealSIV(key, aad, plaintext)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("SIV encryption is not deterministic")
	}
	if len(first) != len(plaintext)+sivTagSize {
		t.Fatalf("sealed length %d; want %d", len(first), len(plaintext)+sivTagSize)
	}
	got, err := openSIV(key, aad, first)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("open = %q, %v", got, err)
	}
	first[len(first)-1] ^= 1
	if _, err := openSIV(key, aad, first); err == nil {
		t.Fatal("tampered ciphertext authenticated")
	}
	first[len(first)-1] ^= 1
	if _, err := openSIV(key, []byte("wrong state"), first); err == nil {
		t.Fatal("wrong associated data authenticated")
	}
}

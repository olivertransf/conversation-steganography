package decalgo

import (
	"encoding/hex"
	"reflect"
	"testing"
)

func TestPBKDF2SHA256Vector(t *testing.T) {
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 2, 32)
	want, _ := hex.DecodeString("ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %x", got)
	}
}

func TestPhraseKeyIsConversationSeparated(t *testing.T) {
	a, err := DeriveKeyFromPhrase("correct horse battery staple", "friends")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DeriveKeyFromPhrase("correct horse battery staple", "work")
	if reflect.DeepEqual(a, b) {
		t.Fatal("different conversations derived the same key")
	}
	if _, err := DeriveKeyFromPhrase("too short", "friends"); err == nil {
		t.Fatal("weak phrase accepted")
	}
}

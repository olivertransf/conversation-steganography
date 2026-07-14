package decalgo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const phraseKDFIterations = 600000

// DeriveKeyFromPhrase turns an exactly shared UTF-8 phrase into a 256-bit key.
// The conversation ID is a public salt, so the same phrase derives independent
// keys for different conversations. The phrase itself is never persisted.
func DeriveKeyFromPhrase(phrase, conversation string) ([]byte, error) {
	if len(phrase) < 16 {
		return nil, errors.New("secret phrase must contain at least 16 characters")
	}
	if conversation == "" {
		return nil, errors.New("conversation identifier is required")
	}
	salt := []byte("decalgo-shared-phrase-v1\x00" + conversation)
	return pbkdf2SHA256([]byte(phrase), salt, phraseKDFIterations, 32), nil
}

func pbkdf2SHA256(password, salt []byte, iterations, length int) []byte {
	out := make([]byte, 0, length)
	for block := uint32(1); len(out) < length; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		var number [4]byte
		binary.BigEndian.PutUint32(number[:], block)
		mac.Write(number[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		remaining := length - len(out)
		if remaining > len(t) {
			remaining = len(t)
		}
		out = append(out, t[:remaining]...)
	}
	return out
}

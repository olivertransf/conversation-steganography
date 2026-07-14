package decalgo

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

// SealCarrierPayload authenticates and encrypts a payload before generative
// encoding. The conversation, direction, and sequence number are bound as AAD.
func SealCarrierPayload(key []byte, conversation, direction string, sequence uint64, plaintext []byte) ([]byte, error) {
	if len(key) < 16 {
		return nil, errors.New("carrier key must contain at least 16 bytes of entropy")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	aad := carrierAAD(conversation, direction, sequence)
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, len(nonce)+len(ciphertext))
	copy(out, nonce)
	copy(out[len(nonce):], ciphertext)
	return out, nil
}

// OpenCarrierPayload verifies a secure carrier and enforces its expected
// conversation direction and sequence number.
func OpenCarrierPayload(key []byte, conversation, direction string, expectedSequence uint64, sealed []byte) ([]byte, error) {
	if len(key) < 16 {
		return nil, errors.New("carrier key must contain at least 16 bytes of entropy")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	minimum := aead.NonceSize() + aead.Overhead()
	if len(sealed) < minimum {
		return nil, errors.New("invalid secure carrier frame")
	}
	nonce := sealed[:aead.NonceSize()]
	ciphertext := sealed[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, carrierAAD(conversation, direction, expectedSequence))
	if err != nil {
		return nil, errors.New("carrier authentication failed")
	}
	return plaintext, nil
}

func carrierAAD(conversation, direction string, sequence uint64) []byte {
	b := make([]byte, 0, len(conversation)+len(direction)+32)
	b = append(b, "decalgo-generative-v1\x00"...)
	b = append(b, conversation...)
	b = append(b, 0)
	b = append(b, direction...)
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], sequence)
	return append(b, seq[:]...)
}

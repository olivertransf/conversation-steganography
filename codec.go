package decalgo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Envelope is deliberately explicit. It protects message contents without
// pretending ciphertext is ordinary human conversation.
type Envelope struct {
	Version int    `json:"v"`
	Index   uint64 `json:"i"`
	Nonce   string `json:"n"`
	Body    string `json:"b"`
}

const wirePrefix = "enc:DEC1."

type Encoder struct {
	aead  cipher.AEAD
	chain [32]byte
	index uint64
}

type Decoder struct {
	aead  cipher.AEAD
	chain [32]byte
	index uint64
}

func derive(key []byte, label string) [32]byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("decalgo-v1:" + label))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	k := derive(key, "message-key")
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func NewEncoder(key []byte, conversationID string) (*Encoder, error) {
	if len(key) < 16 {
		return nil, errors.New("key must contain at least 16 bytes of entropy")
	}
	a, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Encoder{aead: a, chain: derive(key, "conversation:"+conversationID)}, nil
}

func NewDecoder(key []byte, conversationID string) (*Decoder, error) {
	if len(key) < 16 {
		return nil, errors.New("key must contain at least 16 bytes of entropy")
	}
	a, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Decoder{aead: a, chain: derive(key, "conversation:"+conversationID)}, nil
}

func associatedData(index uint64, chain [32]byte) []byte {
	b := make([]byte, 40)
	binary.BigEndian.PutUint64(b[:8], index)
	copy(b[8:], chain[:])
	return b
}

func advance(chain [32]byte, index uint64, nonce, ciphertext []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("decalgo-chain-v1"))
	h.Write(chain[:])
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], index)
	h.Write(n[:])
	h.Write(nonce)
	h.Write(ciphertext)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (e *Encoder) Encode(plaintext string) (string, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := e.aead.Seal(nil, nonce, []byte(plaintext), associatedData(e.index, e.chain))
	env := Envelope{1, e.index, base64.RawURLEncoding.EncodeToString(nonce), base64.RawURLEncoding.EncodeToString(ct)}
	b, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	e.chain = advance(e.chain, e.index, nonce, ct)
	e.index++
	return wirePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func (d *Decoder) Decode(encoded string) (string, error) {
	if len(encoded) < len(wirePrefix) || encoded[:len(wirePrefix)] != wirePrefix {
		return "", errors.New("not a marked DEC1 envelope")
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded[len(wirePrefix):])
	if err != nil {
		return "", fmt.Errorf("invalid envelope: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return "", fmt.Errorf("invalid envelope: %w", err)
	}
	if env.Version != 1 {
		return "", fmt.Errorf("unsupported version %d", env.Version)
	}
	if env.Index != d.index {
		return "", fmt.Errorf("unexpected message index %d; want %d", env.Index, d.index)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) != d.aead.NonceSize() {
		return "", errors.New("invalid nonce")
	}
	ct, err := base64.RawURLEncoding.DecodeString(env.Body)
	if err != nil {
		return "", errors.New("invalid ciphertext")
	}
	pt, err := d.aead.Open(nil, nonce, ct, associatedData(d.index, d.chain))
	if err != nil {
		return "", errors.New("authentication failed")
	}
	d.chain = advance(d.chain, d.index, nonce, ct)
	d.index++
	return string(pt), nil
}

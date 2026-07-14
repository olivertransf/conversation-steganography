package decalgo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
)

const sivTagSize = aes.BlockSize

// sealSIV implements deterministic AES-SIV (RFC 5297) with independent
// AES-256 MAC and encryption keys derived from the conversation key.
func sealSIV(key, aad, plaintext []byte) ([]byte, error) {
	macKey := deriveSIVKey(key, "decalgo-aes-siv-mac-v1")
	encKey := deriveSIVKey(key, "decalgo-aes-siv-enc-v1")
	tag, err := s2v(macKey, aad, plaintext)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	iv := append([]byte(nil), tag...)
	iv[8] &= 0x7f
	iv[12] &= 0x7f
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCTR(block, iv).XORKeyStream(ciphertext, plaintext)
	return append(tag, ciphertext...), nil
}

func openSIV(key, aad, sealed []byte) ([]byte, error) {
	if len(sealed) < sivTagSize {
		return nil, errors.New("invalid SIV ciphertext")
	}
	macKey := deriveSIVKey(key, "decalgo-aes-siv-mac-v1")
	encKey := deriveSIVKey(key, "decalgo-aes-siv-enc-v1")
	tag := sealed[:sivTagSize]
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	iv := append([]byte(nil), tag...)
	iv[8] &= 0x7f
	iv[12] &= 0x7f
	plaintext := make([]byte, len(sealed)-sivTagSize)
	cipher.NewCTR(block, iv).XORKeyStream(plaintext, sealed[sivTagSize:])
	want, err := s2v(macKey, aad, plaintext)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(tag, want) != 1 {
		return nil, errors.New("SIV authentication failed")
	}
	return plaintext, nil
}

func deriveSIVKey(key []byte, label string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	return h.Sum(nil)
}

func s2v(macKey, aad, plaintext []byte) ([]byte, error) {
	d, err := aesCMAC(macKey, make([]byte, aes.BlockSize))
	if err != nil {
		return nil, err
	}
	aadMAC, err := aesCMAC(macKey, aad)
	if err != nil {
		return nil, err
	}
	d = xorBlock(doubleBlock(d), aadMAC)
	if len(plaintext) >= aes.BlockSize {
		input := append([]byte(nil), plaintext...)
		start := len(input) - aes.BlockSize
		for i := 0; i < aes.BlockSize; i++ {
			input[start+i] ^= d[i]
		}
		return aesCMAC(macKey, input)
	}
	padded := make([]byte, aes.BlockSize)
	copy(padded, plaintext)
	padded[len(plaintext)] = 0x80
	return aesCMAC(macKey, xorBlock(doubleBlock(d), padded))
}

func aesCMAC(key, message []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	zero := make([]byte, aes.BlockSize)
	l := make([]byte, aes.BlockSize)
	block.Encrypt(l, zero)
	k1 := doubleBlock(l)
	k2 := doubleBlock(k1)
	blocks := (len(message) + aes.BlockSize - 1) / aes.BlockSize
	complete := len(message) > 0 && len(message)%aes.BlockSize == 0
	if blocks == 0 {
		blocks = 1
	}
	last := make([]byte, aes.BlockSize)
	if complete {
		copy(last, message[(blocks-1)*aes.BlockSize:])
		last = xorBlock(last, k1)
	} else {
		remaining := len(message) - (blocks-1)*aes.BlockSize
		if remaining > 0 {
			copy(last, message[(blocks-1)*aes.BlockSize:])
		}
		last[remaining] = 0x80
		last = xorBlock(last, k2)
	}
	x := make([]byte, aes.BlockSize)
	for i := 0; i < blocks-1; i++ {
		x = xorBlock(x, message[i*aes.BlockSize:(i+1)*aes.BlockSize])
		block.Encrypt(x, x)
	}
	x = xorBlock(x, last)
	block.Encrypt(x, x)
	return x, nil
}

func doubleBlock(in []byte) []byte {
	out := make([]byte, aes.BlockSize)
	carry := byte(0)
	for i := aes.BlockSize - 1; i >= 0; i-- {
		next := in[i] >> 7
		out[i] = in[i]<<1 | carry
		carry = next
	}
	if carry != 0 {
		out[aes.BlockSize-1] ^= 0x87
	}
	return out
}

func xorBlock(a, b []byte) []byte {
	out := make([]byte, aes.BlockSize)
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

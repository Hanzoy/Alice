package securestore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

type Box struct{ aead cipher.AEAD }

func Open(keyPath string) (*Box, error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("Alice master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Encrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}
func (b *Box) Decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	sealed, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", fmt.Errorf("encrypted secret is truncated")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	return string(plain), err
}

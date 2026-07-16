package audit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

func EncryptFromEnvironment(plaintext []byte) ([]byte, error) {
	encoded := os.Getenv("GOALFORGE_AUDIT_KEY")
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("GOALFORGE_AUDIT_KEY must be base64 encoded")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("GOALFORGE_AUDIT_KEY must decode to 16, 24, or 32 bytes")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

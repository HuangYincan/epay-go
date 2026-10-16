package observerstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"strconv"

	"github.com/example/epay-go/pkg/observer"
)

type CredentialScope struct {
	KeyID, Environment, ReceiverID, DeviceID, Provider, SourceClass string
	BindingVersion                                                  int
}
type KeyCipher struct {
	id   string
	aead cipher.AEAD
}

// NewKeyCipher takes ownership of material and wipes it, including invalid input.
func NewKeyCipher(id string, material []byte) (*KeyCipher, error) {
	defer clear(material)
	if !observer.ValidIdentifier(id) || len(material) != 32 {
		return nil, ErrInput
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, ErrInput
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrStorage
	}
	return &KeyCipher{id: id, aead: aead}, nil
}
func (c *KeyCipher) aad(s CredentialScope) ([]byte, error) {
	if c == nil || !observer.ValidIdentifier(s.KeyID) || !observer.ValidIdentifier(s.Environment) || !observer.ValidIdentifier(s.ReceiverID) || !observer.ValidIdentifier(s.DeviceID) || s.BindingVersion < 1 || s.BindingVersion > 2147483647 || (s.Provider != "wxpay" && s.Provider != "alipay") || (s.SourceClass != "synthetic" && s.SourceClass != "notification") {
		return nil, ErrInput
	}
	return json.Marshal([]string{"observer-credential-v1", s.KeyID, s.Environment, s.ReceiverID, strconv.Itoa(s.BindingVersion), s.DeviceID, s.Provider, s.SourceClass, c.id})
}
func (c *KeyCipher) Seal(scope CredentialScope, key []byte) ([]byte, error) {
	aad, err := c.aad(scope)
	if err != nil || len(key) != 32 {
		return nil, ErrInput
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrStorage
	}
	return c.aead.Seal(nonce, nonce, key, aad), nil
}
func (c *KeyCipher) Open(scope CredentialScope, kekID string, ciphertext []byte) ([]byte, error) {
	aad, err := c.aad(scope)
	if err != nil || kekID != c.id || len(ciphertext) != 60 {
		return nil, ErrStorage
	}
	key, err := c.aead.Open(nil, ciphertext[:12], ciphertext[12:], aad)
	if err != nil {
		return nil, ErrStorage
	}
	return key, nil
}

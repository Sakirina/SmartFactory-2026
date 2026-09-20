package identity

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"competition2026/product/platform/internal/store"
	"golang.org/x/crypto/hkdf"
)

// CredentialEnvelope is sealed to the registered edge encryption key. TOTP
// secrets are re-encrypted with the edge's own master key before database commit.
type CredentialEnvelope struct {
	Ephemeral  string `json:"ephemeral"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func (m *Manager) wrappingKey() (*ecdh.PrivateKey, error) {
	seed := sha256.Sum256(append(append([]byte{}, m.Master...), []byte("smartfactory/permission-envelope/v1")...))
	return ecdh.X25519().NewPrivateKey(seed[:])
}
func (m *Manager) EncryptionPublicKey() (string, error) {
	key, e := m.wrappingKey()
	if e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}
func envelopeContext(b PermissionBundle) []byte {
	return []byte(fmt.Sprintf("sf-permissions-v1:%s:%d:%d:%d", b.NodeID, b.Version, b.IssuedMS, b.ExpiresMS))
}
func envelopeAEAD(shared, binding []byte) (cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, e := io.ReadFull(hkdf.New(sha256.New, shared, nil, binding), key); e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func (m *Manager) SignBundleForNode(ctx context.Context, node string, version int64, encryptionPublicKey string) (PermissionBundle, error) {
	b, e := m.SignBundle(ctx, node, version)
	if e != nil {
		return b, e
	}
	encoded, e := base64.StdEncoding.DecodeString(encryptionPublicKey)
	if e != nil {
		return b, e
	}
	target, e := ecdh.X25519().NewPublicKey(encoded)
	if e != nil {
		return b, e
	}
	ephemeral, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return b, e
	}
	shared, e := ephemeral.ECDH(target)
	if e != nil {
		return b, e
	}
	credentials := map[string]Credential{}
	for _, user := range b.Users {
		d, e := m.Store.Get(ctx, "credential", user.ID)
		if e == store.ErrNotFound {
			continue
		}
		if e != nil {
			return b, e
		}
		c, e := store.Decode[Credential](d)
		if e != nil {
			return b, e
		}
		c.TOTP, e = m.Decrypt(user.ID, c.TOTP)
		if e != nil {
			return b, e
		}
		credentials[user.ID] = c
	}
	raw, e := json.Marshal(credentials)
	if e != nil {
		return b, e
	}
	a, e := envelopeAEAD(shared, envelopeContext(b))
	if e != nil {
		return b, e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return b, e
	}
	b.Credentials = &CredentialEnvelope{Ephemeral: base64.StdEncoding.EncodeToString(ephemeral.PublicKey().Bytes()), Nonce: base64.StdEncoding.EncodeToString(nonce), Ciphertext: base64.StdEncoding.EncodeToString(a.Seal(nil, nonce, raw, envelopeContext(b)))}
	b.Signature = ""
	raw, _ = json.Marshal(b)
	b.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(m.Store.SignKey, raw))
	return b, nil
}
func (m *Manager) openCredentials(b PermissionBundle) (map[string]Credential, error) {
	if b.Credentials == nil {
		return nil, nil
	}
	key, e := m.wrappingKey()
	if e != nil {
		return nil, e
	}
	raw, e := base64.StdEncoding.DecodeString(b.Credentials.Ephemeral)
	if e != nil {
		return nil, e
	}
	ephemeral, e := ecdh.X25519().NewPublicKey(raw)
	if e != nil {
		return nil, e
	}
	shared, e := key.ECDH(ephemeral)
	if e != nil {
		return nil, e
	}
	a, e := envelopeAEAD(shared, envelopeContext(b))
	if e != nil {
		return nil, e
	}
	nonce, e := base64.StdEncoding.DecodeString(b.Credentials.Nonce)
	if e != nil || len(nonce) != a.NonceSize() {
		return nil, ErrDenied
	}
	ciphertext, e := base64.StdEncoding.DecodeString(b.Credentials.Ciphertext)
	if e != nil {
		return nil, e
	}
	raw, e = a.Open(nil, nonce, ciphertext, envelopeContext(b))
	if e != nil {
		return nil, e
	}
	credentials := map[string]Credential{}
	if e = json.Unmarshal(raw, &credentials); e != nil {
		return nil, e
	}
	for id, c := range credentials {
		c.TOTP, e = m.Encrypt(id, c.TOTP)
		if e != nil {
			return nil, e
		}
		credentials[id] = c
	}
	return credentials, nil
}

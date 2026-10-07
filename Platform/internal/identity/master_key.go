package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LoadMasterKey opens the existing node encryption key or creates it once.
func LoadMasterKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("master key file is required")
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.WriteString(base64.StdEncoding.EncodeToString(key))
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		return key, e
	}
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("master key must be an owner-only regular file")
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if e != nil || len(key) != 32 {
		return nil, errors.New("master key must be a base64 encoded 32-byte value")
	}
	return key, nil
}

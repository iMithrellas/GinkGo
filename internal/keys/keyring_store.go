package keys

import (
	"encoding/base64"
	"errors"

	"github.com/zalando/go-keyring"
)

const DefaultKeyringService = "ginkgo"

// KeyringStore keeps key material in the system keyring.
type KeyringStore struct {
	Service string
}

func (s *KeyringStore) Get(id string) ([]byte, error) {
	service := s.service()
	val, err := keyring.Get(service, id)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}
	out, err := base64.StdEncoding.DecodeString(val)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *KeyringStore) Put(id string, key []byte) error {
	service := s.service()
	val := base64.StdEncoding.EncodeToString(key)
	return keyring.Set(service, id, val)
}

func (s *KeyringStore) Delete(id string) error {
	service := s.service()
	err := keyring.Delete(service, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func (s *KeyringStore) service() string {
	if s != nil && s.Service != "" {
		return s.Service
	}
	return DefaultKeyringService
}

// KeyringAvailable reports whether a system keyring backend appears supported.
func KeyringAvailable() bool {
	_, err := keyring.Get(DefaultKeyringService, "_probe_")
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return true
	}
	return !errors.Is(err, keyring.ErrUnsupported)
}

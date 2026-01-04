package keys

import (
	"encoding/base64"
	"errors"
)

// KeyStore provides access to namespace key material.
type KeyStore interface {
	Get(id string) ([]byte, error)
	Put(id string, key []byte) error
	Delete(id string) error
}

var ErrKeyNotFound = errors.New("key not found")

// ConfigStore keeps key material in config-managed storage.
type ConfigStore struct {
	Keys map[string]string
}

func (s *ConfigStore) Get(id string) ([]byte, error) {
	if s == nil || s.Keys == nil {
		return nil, ErrKeyNotFound
	}
	val, ok := s.Keys[id]
	if !ok || val == "" {
		return nil, ErrKeyNotFound
	}
	out, err := base64.StdEncoding.DecodeString(val)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ConfigStore) Put(id string, key []byte) error {
	if s.Keys == nil {
		s.Keys = map[string]string{}
	}
	s.Keys[id] = base64.StdEncoding.EncodeToString(key)
	return nil
}

func (s *ConfigStore) Delete(id string) error {
	if s == nil || s.Keys == nil {
		return nil
	}
	delete(s.Keys, id)
	return nil
}

package keys

import (
	"bytes"
	"testing"
)

func TestConfigStoreRoundTrip(t *testing.T) {
	store := &ConfigStore{}
	keyID := "ns/default/read"
	value := []byte("secret")

	if err := store.Put(keyID, value); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	got, err := store.Get(keyID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("get mismatch: got %q want %q", got, value)
	}
	if err := store.Delete(keyID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	_, err = store.Get(keyID)
	if err == nil {
		t.Fatalf("expected missing key error")
	}
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

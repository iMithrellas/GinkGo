package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestCheckConfigValidityValid(t *testing.T) {
	v := viper.New()
	v.Set("namespace", "default")
	v.Set("data_dir", "/tmp/ginkgo")
	v.Set("export.page_size", 100)
	v.Set("sync.batch_size", 50)
	v.Set("notifications.enabled", false)

	if err := CheckConfigValidity(v); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestCheckConfigValidityInvalid(t *testing.T) {
	v := viper.New()
	v.Set("namespace", "")
	v.Set("data_dir", "")
	v.Set("export.page_size", 0)
	v.Set("sync.batch_size", 0)
	v.Set("notifications.enabled", true)
	v.Set("notifications.every_days", 0)
	v.Set("remotes.origin.url", "not a url")
	v.Set("remotes.origin.token", "")
	v.Set("remotes.origin.enabled", true)
	v.Set("namespaces.work.e2ee", true)
	v.Set("namespaces.work.key_provider", "config")
	v.Set("namespaces.work.read_key", "bad")
	v.Set("namespaces.work.signer_key_provider", "config")

	err := CheckConfigValidity(v)
	if err == nil {
		t.Fatalf("expected error for invalid config")
	}

	msg := err.Error()
	expected := []string{
		"namespace is required",
		"data_dir is required",
		"export.page_size must be greater than 0",
		"sync.batch_size must be greater than 0",
		"notifications.every_days must be greater than 0",
		"remote origin has invalid url",
		"remote origin missing token",
		"namespace work missing write_key",
		"namespace work read_key must be base64",
		"namespace work missing signer_priv",
	}
	for _, want := range expected {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected error to contain %q, got %q", want, msg)
		}
	}
}

package config

import (
	"strings"
	"testing"
)

func TestUpsertNamespaceConfigAppend(t *testing.T) {
	input := strings.TrimSpace(`
[core]
namespace = "default"
`)
	values := map[string]any{
		"e2ee":         true,
		"key_provider": "config",
		"read_key":     "cmVhZA==",
		"write_key":    "d3JpdGU=",
	}
	got, _ := UpsertNamespaceConfig(input, "work", values)
	if !strings.Contains(got, "[namespaces.work]") {
		t.Fatalf("missing namespace section:\n%s", got)
	}
	if !strings.Contains(got, "e2ee = true") {
		t.Fatalf("missing e2ee:\n%s", got)
	}
}

func TestUpsertNamespaceConfigReplace(t *testing.T) {
	input := strings.TrimSpace(`
[namespaces.work]
e2ee = false
key_provider = "config"
read_key = "old"
`)
	values := map[string]any{
		"e2ee":         true,
		"key_provider": "config",
		"read_key":     "new",
	}
	got, _ := UpsertNamespaceConfig(input, "work", values)
	if strings.Contains(got, "read_key = \"old\"") {
		t.Fatalf("old value not replaced:\n%s", got)
	}
	if !strings.Contains(got, "read_key = \"new\"") {
		t.Fatalf("new value missing:\n%s", got)
	}
}

func TestDeleteNamespaceConfig(t *testing.T) {
	input := strings.TrimSpace(`
[core]
namespace = "default"

[namespaces.work]
e2ee = true

[namespaces.play]
e2ee = false
`)
	got, removed := DeleteNamespaceConfig(input, "work")
	if !removed {
		t.Fatalf("expected removal")
	}
	if strings.Contains(got, "[namespaces.work]") {
		t.Fatalf("namespace still present:\n%s", got)
	}
	if !strings.Contains(got, "[namespaces.play]") {
		t.Fatalf("other namespace removed:\n%s", got)
	}
}

//go:build linux

package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic-only regressions for credential and policy isolation.
func TestClaudePolicyRejectsHardlinkAlias(t *testing.T) {
	dir := t.TempDir()
	credentials := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"claudeAiOauth":{"accessToken":"synthetic-never-valid"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(dir, "remote-settings.json")
	if err := os.Link(credentials, policy); err != nil {
		t.Fatal(err)
	}
	inspection := &claudePolicyInspection{ctx: context.Background()}
	if err := inspection.file(policy); !errors.Is(err, ErrClaudeCodeIsolation) || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("policy reader accepted or disclosed a credential alias", err)
	}
	if inspection.files != 0 || inspection.bytes != 0 {
		t.Fatal("credential alias rejection happened after inspection began")
	}
}

func TestClaudePolicyRejectsParentLinkAlias(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "unrelated.json"), []byte(`{"secret":"synthetic-only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, "managed-settings.d")); err != nil {
		t.Fatal(err)
	}
	if err := claudeEndpointPolicy(context.Background(), dir); !errors.Is(err, ErrClaudeCodeIsolation) {
		t.Fatal("policy directory followed a parent alias", err)
	}
	// The selected config itself, or an earlier ancestor, must not be traversed
	// through a link either. No fallback/relocation of the native profile occurs.
	link := filepath.Join(dir, "profile")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err := claudePolicyFile(context.Background(), filepath.Join(link, "remote-settings.json")); !errors.Is(err, ErrClaudeCodeIsolation) {
		t.Fatal("selected profile parent alias accepted", err)
	}
}

func TestClaudeCodePolicyDropinNativeNamesAndTypes(t *testing.T) {
	dir := t.TempDir()
	dropins := filepath.Join(dir, "managed-settings.d")
	if err := os.Mkdir(dropins, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".credentials.json", ".hidden.json", "ignored.txt", "ignored.JSON"} {
		if err := os.WriteFile(filepath.Join(dropins, name), []byte(`not policy JSON`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dropins, "directory.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropins, "valid.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	inspection := &claudePolicyInspection{ctx: context.Background()}
	if err := inspection.endpoint(dir); err != nil || inspection.files != 1 || inspection.bytes != 2 {
		t.Fatalf("native ignored entries were inspected: files=%d bytes=%d err=%v", inspection.files, inspection.bytes, err)
	}
	// Native considers non-dot symlink .json entries; refuse rather than silently
	// skipping or following such a mandatory policy entry.
	if err := os.Symlink("valid.json", filepath.Join(dropins, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if err := claudeEndpointPolicy(context.Background(), dir); !errors.Is(err, ErrClaudeCodeIsolation) {
		t.Fatal("symlink drop-in was silently skipped or followed", err)
	}
}

func TestClaudeCodePolicyInspectionBudgets(t *testing.T) {
	for _, mode := range []string{"files", "entries", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dropins := filepath.Join(dir, "managed-settings.d")
			if err := os.Mkdir(dropins, 0700); err != nil {
				t.Fatal(err)
			}
			count, suffix, content := claudePolicyMaxFiles+1, ".json", []byte(`{}`)
			switch mode {
			case "entries":
				count, suffix = claudePolicyMaxEntries+1, ".ignored"
			case "bytes":
				count = 3
				content = append([]byte(`{}`), bytes.Repeat([]byte(" "), claudePolicyMaxFileBytes-2)...)
			}
			for i := 0; i < count; i++ {
				if err := os.WriteFile(filepath.Join(dropins, fmt.Sprintf("policy-%03d%s", i, suffix)), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			inspection := &claudePolicyInspection{ctx: context.Background()}
			err := inspection.endpoint(dir)
			if !errors.Is(err, ErrClaudeCodeIsolation) || !strings.Contains(err.Error(), "budget") {
				t.Fatal("unbounded policy inspection", err)
			}
			if inspection.bytes > claudePolicyMaxBytes || inspection.files > claudePolicyMaxFiles+1 || inspection.entries > claudePolicyMaxEntries+1 {
				t.Fatalf("inspection exceeded bounds: %+v", inspection)
			}
		})
	}
}

func TestClaudeCodePolicyAggregateBudgetIncludesAllSources(t *testing.T) {
	dir := t.TempDir()
	content := append([]byte(`{}`), bytes.Repeat([]byte(" "), claudePolicyMaxFileBytes-2)...)
	for _, name := range []string{"managed-settings.json", "managed-mcp.json", "remote-settings.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	inspection := &claudePolicyInspection{ctx: context.Background()}
	if err := inspection.endpoint(dir); err != nil {
		t.Fatal(err)
	}
	if err := inspection.file(filepath.Join(dir, "remote-settings.json")); !errors.Is(err, ErrClaudeCodeIsolation) || !strings.Contains(err.Error(), "budget") || inspection.bytes != claudePolicyMaxBytes {
		t.Fatal("aggregate inspection budget reset between policy sources", err)
	}
}

func TestClaudeCodePolicyDropinDescriptorRemainsAnchored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed-settings.d")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "policy.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	opened, err := claudePolicyDirectory(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := os.Rename(path, filepath.Join(dir, "original")); err != nil {
		t.Fatal(err)
	}
	unrelated := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrelated, "policy.json"), []byte(`unrelated-invalid-private-data`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unrelated, path); err != nil {
		t.Fatal(err)
	}
	inspection := &claudePolicyInspection{ctx: context.Background()}
	if err := inspection.fileAt(opened, "policy.json"); err != nil || inspection.bytes != 2 {
		t.Fatal("descriptor traversal followed a replaced parent", err)
	}
}

type claudePolicyCancelContext struct {
	context.Context
	cancel context.CancelFunc
	when   func() bool
}

func (c claudePolicyCancelContext) Err() error {
	if c.when() {
		c.cancel()
	}
	return c.Context.Err()
}

func TestClaudeCodePolicyInspectionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := claudeLocalIsolation(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-start cancellation lost", err)
	}
	for _, mode := range []string{"scan", "read"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dropins := filepath.Join(dir, "managed-settings.d")
			if err := os.Mkdir(dropins, 0700); err != nil {
				t.Fatal(err)
			}
			content := append([]byte(`{}`), bytes.Repeat([]byte(" "), 100000)...)
			for i := 0; i < 4; i++ {
				if err := os.WriteFile(filepath.Join(dropins, fmt.Sprintf("policy-%d.json", i)), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inspection := &claudePolicyInspection{}
			inspection.ctx = claudePolicyCancelContext{Context: ctx, cancel: cancel, when: func() bool {
				if mode == "scan" {
					return inspection.files >= 2
				}
				return inspection.bytes >= 32<<10
			}}
			if err := inspection.endpoint(dir); !errors.Is(err, context.Canceled) {
				t.Fatal("mid-inspection cancellation lost", err)
			}
			if (mode == "scan" && inspection.files != 2) || (mode == "read" && inspection.bytes != 32<<10) {
				t.Fatalf("cancellation did not stop at checkpoint: %+v", inspection)
			}
		})
	}
}

func TestClaudeCodePolicyDirectoryErrorsNotIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "managed-settings.d"), []byte(`PRIVATE`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := claudeEndpointPolicy(context.Background(), dir); !errors.Is(err, ErrClaudeCodeIsolation) || strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("directory I/O failure was ignored or disclosed", err)
	}
}

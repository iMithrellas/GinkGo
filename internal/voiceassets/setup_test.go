package voiceassets

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func testAsset(data string) Asset {
	return Asset{Key: "voice.sherpa.parakeet.encoder", Path: "parakeet/encoder.onnx", Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(data)))}
}

func TestProvisionVerifiedAtomicAndOffline(t *testing.T) {
	const data = "verified model bytes"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, data)
	}))
	defer server.Close()
	a := testAsset(data)
	a.URL = server.URL
	dir := t.TempDir()
	location := Location{Asset: a, Filename: filepath.Join(dir, a.Path)}
	ctx := context.Background()
	require.Error(t, Provision(ctx, []Location{location}, true, io.Discard))
	require.Zero(t, requests.Load(), "--check must not download missing files")
	_, err := os.Stat(filepath.Dir(location.Filename))
	require.True(t, os.IsNotExist(err), "--check must not create directories")
	require.NoError(t, Provision(ctx, []Location{location}, false, io.Discard))
	server.Close()
	require.NoError(t, Provision(ctx, []Location{location}, true, io.Discard))
	require.NoError(t, Provision(ctx, []Location{location}, false, io.Discard))
	require.EqualValues(t, 1, requests.Load())
	files, err := os.ReadDir(filepath.Dir(location.Filename))
	require.NoError(t, err)
	require.Len(t, files, 1, "staging files must be removed")
	info, err := os.Stat(location.Filename)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, os.WriteFile(location.Filename, []byte("corruption"), 0600))
	require.ErrorContains(t, Provision(ctx, []Location{location}, false, io.Discard), "SHA-256")
	got, err := os.ReadFile(location.Filename)
	require.NoError(t, err)
	require.Equal(t, "corruption", string(got), "do not silently overwrite installed files")
}

func TestProvisionRejectsInvalidDownloadsAndPreservesOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
	}{
		{"checksum", "wrong bytes", http.StatusOK},
		{"truncated", "", http.StatusOK},
		{"oversized", strings.Repeat("a", 100), http.StatusOK},
		{"http-error", "verified model bytes", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			a := testAsset("verified model bytes")
			a.URL = server.URL
			dir := t.TempDir()
			location := Location{Asset: a, Filename: filepath.Join(dir, "model")}
			require.Error(t, Provision(context.Background(), []Location{location}, false, io.Discard))
			files, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, files, "failed downloads must not publish or leave staging files")
		})
	}
	location := Location{Asset: testAsset("profile"), Filename: filepath.Join(t.TempDir(), "custom.onnx"), Override: true}
	require.Error(t, Provision(context.Background(), []Location{location}, false, io.Discard))
	require.NoError(t, os.WriteFile(location.Filename, []byte("custom model"), 0600))
	require.NoError(t, Provision(context.Background(), []Location{location}, false, io.Discard))
	got, err := os.ReadFile(location.Filename)
	require.NoError(t, err)
	require.Equal(t, "custom model", string(got))
	location.Key = "voice.sherpa.kws.encoder"
	require.ErrorContains(t, Verify(context.Background(), location), "SHA-256", "custom KWS cannot bypass the audited hashes")
}

func TestCanceledSetupAndDanglingSymlink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	a := testAsset("model")
	a.Content = "model"
	l := Location{Asset: a, Filename: filepath.Join(dir, "model")}
	require.ErrorIs(t, Provision(ctx, []Location{l}, false, io.Discard), context.Canceled)
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing"), l.Filename))
	require.ErrorContains(t, Provision(context.Background(), []Location{l}, false, io.Discard), "already exists")
	_, err := os.Lstat(l.Filename)
	require.NoError(t, err)
}

func TestArchiveOnlyReadsExactRegularMember(t *testing.T) {
	for _, kind := range []byte{tar.TypeReg, tar.TypeSymlink} {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		a := testAsset("model")
		a.Member = "pinned/model.onnx"
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../../escape", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}))
		h := &tar.Header{Name: a.Member, Typeflag: kind, Size: a.Size}
		if kind == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "../../escape"
		}
		require.NoError(t, tw.WriteHeader(h))
		if kind == tar.TypeReg {
			_, err := tw.Write([]byte("model"))
			require.NoError(t, err)
		}
		require.NoError(t, tw.Close())
		var out bytes.Buffer
		err := extractMember(context.Background(), tar.NewReader(&archive), a, &out)
		if kind == tar.TypeReg {
			require.NoError(t, err)
			require.Equal(t, "model", out.String())
		} else {
			require.Error(t, err)
			require.Empty(t, out.String())
		}
	}
}

func TestResolveInstalledProfileAndOverrides(t *testing.T) {
	user, installed := t.TempDir(), t.TempDir()
	a := Assets()[0]
	encoder := filepath.Join(installed, a.Path)
	require.NoError(t, os.MkdirAll(filepath.Dir(encoder), 0700))
	require.NoError(t, os.WriteFile(encoder, []byte("installed"), 0600))
	locations := Resolve([]string{user, installed}, map[string]string{"voice.sherpa.kws.tokens": "/explicit/tokens"})
	require.Equal(t, encoder, locations[0].Filename)
	require.Equal(t, filepath.Join(filepath.Dir(encoder), "encoder.weights"), locations[1].Filename)
	require.Error(t, Verify(context.Background(), locations[1]), "missing external weights must not pass verification")
	for _, location := range locations {
		if location.Key == "voice.sherpa.kws.tokens" {
			require.Equal(t, "/explicit/tokens", location.Filename)
			require.True(t, location.Override)
		}
	}
	locations = Resolve([]string{user, installed}, map[string]string{a.Key: "/custom/encoder"})
	require.Equal(t, "/custom/encoder", locations[0].Filename)
	for _, location := range locations {
		require.NotEmpty(t, location.Key, "custom encoder dependencies belong to native initialization")
	}
}

func TestExecutableDiscoveryAndExplicitMissingOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(dir, 0700))
	path := filepath.Join(dir, "ginkgo-test-voice")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), 0700))
	got, err := Executable("ginkgo-test-voice")
	require.NoError(t, err)
	require.Equal(t, path, got)
	_, err = Executable(filepath.Join(home, "missing", "ginkgo-test-voice"))
	require.Error(t, err, "explicit path must not fall back to discovered binary")
}

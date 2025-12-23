package tests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/cli"
	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/daemon"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
)

// runCLI executes the CLI with the given args and returns stdout, stderr, and error.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := cli.NewRootCmd()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return os.ErrNotExist
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestE2E_NoteAdd(t *testing.T) {
	// 1. Setup environment
	tmpDir := t.TempDir()
	runDir := filepath.Join(tmpDir, "run")
	dataDir := filepath.Join(tmpDir, "data")
	os.MkdirAll(runDir, 0o700)
	os.MkdirAll(dataDir, 0o700)

	// Set XDG variables so both Daemon and CLI find the same paths
	t.Setenv("XDG_RUNTIME_DIR", runDir)

	// Create a minimal config file
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	cfgContent := "\n"
	cfgContent += "data_dir: \"" + dataDir + "\"\n"
	cfgContent += "default_namespace: \"test\"\n"
	cfgContent += "http_addr: \"127.0.0.1:0\"\n"
	os.WriteFile(cfgPath, []byte(cfgContent), 0o600)

	// 2. Start daemon
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := viper.New()
	v.SetConfigFile(cfgPath)

	// Force critical test settings to ensure isolation
	require.NoError(t, config.Load(ctx, v))
	v.Set("data_dir", dataDir)
	v.Set("http_addr", "127.0.0.1:0")
	// Explicitly disable remotes to avoid background sync noise
	v.Set("remotes.origin.enabled", false)

	app, err := wire.BuildApp(ctx, v)
	require.NoError(t, err)

	go func() {
		_ = daemon.Run(ctx, app)
	}()

	// Wait for socket to become available
	sock, err := ipc.SocketPath()
	require.NoError(t, err)
	require.NoError(t, waitForSocket(sock, 5*time.Second))

	// 3. Test: Note Add (One-liner)
	t.Run("Add One-Liner", func(t *testing.T) {
		out, _, err := runCLI(t, "--config", cfgPath, "note", "add", "My First Note", "--tags", "alpha,beta")
		require.NoError(t, err)

		// Verify output format: "<id>\t<title>"
		parts := strings.Fields(out)
		require.GreaterOrEqual(t, len(parts), 2)
		id := parts[0]
		title := strings.Join(parts[1:], " ")

		assert.NotEmpty(t, id)
		assert.Equal(t, "My First Note", title)
	})

	// 4. Test: Verify with List
	t.Run("Verify List", func(t *testing.T) {
		out, _, err := runCLI(t, "--config", cfgPath, "note", "list")
		require.NoError(t, err)
		assert.Contains(t, out, "My First Note")
		assert.Contains(t, out, "alpha,beta")
	})
}

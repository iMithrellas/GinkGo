package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func TestRejectedInitExitsWithParentPipeOpen(t *testing.T) {
	if os.Getenv("GINKGO_TEST_REJECTED_INIT") == "1" {
		main()
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRejectedInitExitsWithParentPipeOpen$")
	cmd.Env = append(os.Environ(), "GINKGO_TEST_REJECTED_INIT=1")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer in.Close() // Deliberately leave open until the helper exits on its own.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	graph := voiceworker.Graph{Encoder: "/missing/encoder", Decoder: "/missing/decoder", Joiner: "/missing/joiner", Tokens: "/missing/tokens"}
	cfg := voiceworker.Config{Enabled: true, RecorderExecutable: "/missing/recorder", Parakeet: graph, KWS: graph, KeywordsFile: "/missing/keywords", SileroModel: "/missing/silero", Provider: voiceworker.CPU, UpdateMS: 500, WindowMS: 5000, SilenceMS: 1500, MaxRecordingMS: 60000, TriggerTimeoutMS: 3000}
	require.NoError(t, cfg.Validate())
	require.NoError(t, json.NewEncoder(in).Encode(voiceworker.Command{Version: voiceworker.Version, Type: "init", Epoch: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Config: &cfg}))
	require.Error(t, cmd.Wait())
	require.NoError(t, ctx.Err(), "helper hung joining its inherited stdin reader")
	require.Contains(t, stderr.String(), "ginkgo-voice:")
}

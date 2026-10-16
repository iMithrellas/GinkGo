package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

type aiTestListener struct{ net.Listener }

func (l aiTestListener) Listen(context.Context) (net.Listener, error) { return l.Listener, nil }

func startAICommandFake(t *testing.T, response ipc.Response) (context.Context, <-chan ipc.Message) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	sock, err := ipc.SocketPath()
	require.NoError(t, err)
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	messages := make(chan ipc.Message, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = transport.NewUnixServer(aiTestListener{l}).Serve(ctx, ipc.PBHandler(func(m ipc.Message) ipc.Response {
			messages <- m
			return response
		}))
	}()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	v := viper.New()
	v.Set("namespace", "work")
	v.Set("namespaces.work.e2ee", false)
	return context.WithValue(ctx, appKey, &wire.App{Cfg: v}), messages
}

func TestAINoteCLIFakeIPC(t *testing.T) {
	for _, action := range []string{"create", "expand"} {
		t.Run(action, func(t *testing.T) {
			ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Entry: &api.Entry{ID: "saved", Title: "Saved title"}, Action: "create", FallbackReason: "provider unavailable"})
			cmd := newAICmd()
			cmd.SetContext(ctx)
			input := " original input\nsecond line\n\n"
			cmd.SetIn(strings.NewReader(input))
			var out, diagnostics bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostics)
			args := []string{"note", "--stdin", "--provider", "test", "--title", "override", "--tag", "one", "--tag", "two", "--output", "json", "--timeout", "2m"}
			if action == "create" {
				args = append(args, "--new")
			} else {
				args = append(args, "--note", "target")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			m := <-messages
			require.Equal(t, "ai.note", m.Name)
			require.Equal(t, input, m.Text)
			require.Equal(t, "work", m.Namespace)
			require.Equal(t, "test", m.Provider)
			require.Equal(t, "override", m.Title)
			require.Equal(t, []string{"one", "two"}, m.Tags)
			require.Equal(t, action, m.Action)
			if action == "expand" {
				require.Equal(t, "target", m.NoteID)
			}
			require.WithinDuration(t, time.Now().Add(2*time.Minute), m.Deadline, time.Second)
			var result map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			require.Contains(t, result, "entry")
			require.Contains(t, result, "action")
			require.Contains(t, result, "fallback_reason")
			require.Empty(t, diagnostics.String(), "JSON mode must keep diagnostics structured")
		})
	}
}

func TestAINoteCLITextFallback(t *testing.T) {
	ctx, _ := startAICommandFake(t, ipc.Response{OK: true, Entry: &api.Entry{ID: "saved", Title: "Saved title"}, Action: "create", FallbackReason: "disabled"})
	cmd := newAICmd()
	cmd.SetContext(ctx)
	cmd.SetIn(strings.NewReader("original\n"))
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"note", "--stdin"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "saved\tSaved title\n", out.String())
	require.Contains(t, diagnostics.String(), "disabled")
}

func TestAINoteStdinBounds(t *testing.T) {
	for _, text := range []string{"\n\t ", strings.Repeat("x", maxAINoteInput+1)} {
		_, err := readAINoteInput(strings.NewReader(text))
		require.Error(t, err)
	}
	for _, text := range []string{" exact\ntext\n\n", strings.Repeat("x", maxAINoteInput)} {
		got, err := readAINoteInput(strings.NewReader(text))
		require.NoError(t, err)
		require.Equal(t, text, got)
	}
}

func TestImportAndShowOriginalIPC(t *testing.T) {
	p := &api.Provenance{Sources: []api.Source{{ID: "source", Kind: "text", OriginalText: " exact original\n", CapturedAt: time.Now().UTC()}}}
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Entry: &api.Entry{ID: "saved", Title: "clean", Body: "clean body", Provenance: p}})
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	require.NoError(t, importOne(cmd, api.Entry{Namespace: "work", Body: "clean body", Provenance: p}))
	m := <-messages
	require.Equal(t, "note.add", m.Name)
	require.Equal(t, p, m.Provenance)
	show := newNoteShowCmd()
	show.SetContext(ctx)
	show.SetArgs([]string{"saved", "--original"})
	var out bytes.Buffer
	show.SetOut(&out)
	require.NoError(t, show.Execute())
	require.Equal(t, p.Sources[0].OriginalText, out.String())
}

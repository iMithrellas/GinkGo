package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

func newImportCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import notes from JSON (array or NDJSON)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("--file is required")
			}
			app := getApp(cmd)

			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()

			br := bufio.NewReader(f)
			// Peek first non-space byte to decide array vs NDJSON
			first, err := peekFirstNonSpace(br)
			if err != nil {
				return err
			}

			dec := json.NewDecoder(br)
			imported := 0
			skipped := 0
			now := time.Now().UTC()

			normalize := func(e *api.Entry) {
				// ID intentionally left empty to use normal create flow via daemon.
				// Version will be set by the server.
				if e.CreatedAt.IsZero() {
					e.CreatedAt = now
				}
				if e.UpdatedAt.IsZero() {
					e.UpdatedAt = e.CreatedAt
				}
				if e.Namespace == "" {
					e.Namespace = app.Cfg.GetString("namespace")
				}
			}

			if first == '[' {
				// JSON array
				var arr []api.Entry
				if err := dec.Decode(&arr); err != nil {
					return err
				}
				for i := range arr {
					normalize(&arr[i])
					if err := importOne(cmd, app, arr[i]); err != nil {
						skipped++
						continue
					}
					imported++
				}
			} else {
				// NDJSON stream
				for {
					var e api.Entry
					if err := dec.Decode(&e); err != nil {
						if errors.Is(err, io.EOF) {
							break
						}
						return err
					}
					normalize(&e)
					if err := importOne(cmd, app, e); err != nil {
						skipped++
						continue
					}
					imported++
				}
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Imported: %d\nSkipped (conflict): %d\n", imported, skipped)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "input JSON file (array or NDJSON)")
	return cmd
}

func importOne(cmd *cobra.Command, app *wire.App, e api.Entry) error {
	// Create via daemon; let it generate the ID and normalize tags.
	if err := ensureNamespaceConfigured(cmd, e.Namespace); err != nil {
		return err
	}
	sock, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	m := ipc.Message{
		Name:      "note.add",
		Title:     e.Title,
		Body:      e.Body,
		Tags:      e.Tags,
		Namespace: e.Namespace,
	}
	resp, err := ipc.Request(cmd.Context(), sock, m)
	if err != nil {
		return err
	}
	if !resp.OK || resp.Entry == nil {
		if resp.Msg != "" {
			return errors.New(resp.Msg)
		}
		return fmt.Errorf("failed to import entry")
	}
	return nil
}

func peekFirstNonSpace(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == ' ' || b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		// put it back for the decoder
		if err := r.UnreadByte(); err != nil {
			return 0, err
		}
		return b, nil
	}
}

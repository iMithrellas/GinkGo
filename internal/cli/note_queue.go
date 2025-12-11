package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/pkg/api"
)

type cursorsFile struct {
	PushAfter string `json:"push_after"`
	PullAfter string `json:"pull_after"`
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func loadPushCursor(dataDir, remote string) time.Time {
	p := filepath.Join(dataDir, "sync", "cursor_"+remote+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}
	}
	var cf cursorsFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return time.Time{}
	}
	return parseTS(cf.PushAfter)
}

func newNoteQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "List local events pending sync to remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			rem := app.Cfg.GetStringMap("remotes")
			if len(rem) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no remotes configured; queue is empty")
				return nil
			}
			dataDir := app.Cfg.GetString("data_dir")
			// Stable order of remotes
			names := make([]string, 0, len(rem))
			for name := range rem {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				base := "remotes." + name + "."
				url := strings.TrimSpace(app.Cfg.GetString(base + "url"))
				enabled := app.Cfg.GetBool(base + "enabled")
				if app.Cfg.IsSet(base+"enabled") && !enabled {
					continue
				}
				if url == "" {
					continue
				}
				after := loadPushCursor(dataDir, name)
				total := 0
				const page = 500
				cur := api.Cursor{After: after}
				var last time.Time
				for {
					evs, next, err := app.Store.Events.List(cmd.Context(), cur, page)
					if err != nil {
						return err
					}
					total += len(evs)
					if len(evs) < page {
						break
					}
					cur = next
					if !next.After.IsZero() {
						last = next.After
					}
				}
				// Show header and a small preview of the pending events
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "remote=%s url=%s pending=%d\n", name, url, total)
				// Optionally show sample with first 10 items
				if total > 0 {
					evs, _, _ := app.Store.Events.List(cmd.Context(), api.Cursor{After: after}, 10)
					for _, e := range evs {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s %-6s %s\n", e.Time.UTC().Format(time.RFC3339), string(e.Type), e.ID)
					}
					if total > 10 {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  ... (%d more)\n", total-10)
					}
				}
				_ = last
			}
			return nil
		},
	}
	return cmd
}

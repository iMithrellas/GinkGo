package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
)

func newNoteQueueCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "List local events pending sync to remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 {
				limit = 10
			}
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(context.Background(), sock, ipc.Message{Name: "sync.queue", Limit: limit})
			if err != nil {
				return err
			}
			if !resp.OK {
				return errors.New(resp.Msg)
			}
			if len(resp.Queue) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no remotes configured; queue is empty")
				return nil
			}
			for _, r := range resp.Queue {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "remote=%s url=%s pending=%d\n", r.Name, r.URL, r.Pending)
				for _, e := range r.Events {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s %-6s %s\n", e.Time.UTC().Format(time.RFC3339), e.Type, e.ID)
				}
				if int64(len(r.Events)) < r.Pending {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  ... (%d more)\n", r.Pending-int64(len(r.Events)))
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "l", 10, "max events per remote to show")
	return cmd
}

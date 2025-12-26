package cli

import (
	"context"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func fetchAllEntries(ctx context.Context, sock string, pageSize int, build func(cursor string) ipc.Message) ([]api.Entry, error) {
	if pageSize <= 0 {
		pageSize = 200
	}
	out := make([]api.Entry, 0, pageSize)
	cursor := ""
	for {
		req := build(cursor)
		req.Limit = pageSize
		req.Cursor = cursor
		resp, err := ipc.Request(ctx, sock, req)
		if err != nil {
			return nil, err
		}
		if len(resp.Entries) == 0 {
			break
		}
		out = append(out, resp.Entries...)
		if resp.Page.Next == "" {
			break
		}
		if resp.Page.Next == cursor {
			break
		}
		cursor = resp.Page.Next
	}
	return out, nil
}

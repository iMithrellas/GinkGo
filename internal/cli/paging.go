package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

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

func streamEntries(ctx context.Context, sock string, pageSize int, build func(cursor string) ipc.Message, writer entryStreamWriter) error {
	if pageSize <= 0 {
		pageSize = 200
	}
	cursor := ""
	for {
		req := build(cursor)
		req.Limit = pageSize
		req.Cursor = cursor
		resp, err := ipc.Request(ctx, sock, req)
		if err != nil {
			return err
		}
		if len(resp.Entries) == 0 {
			break
		}
		if err := writer.WriteEntries(resp.Entries); err != nil {
			if isBrokenPipe(err) {
				return nil
			}
			return err
		}
		if resp.Page.Next == "" {
			break
		}
		if resp.Page.Next == cursor {
			break
		}
		cursor = resp.Page.Next
	}
	if err := writer.Close(); err != nil {
		if isBrokenPipe(err) {
			return nil
		}
		return err
	}
	return nil
}

func isBrokenPipe(err error) bool {
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return errors.Is(pathErr.Err, syscall.EPIPE)
	}
	return false
}

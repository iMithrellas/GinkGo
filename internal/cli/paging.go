package cli

import (
	"context"
	"errors"
	"fmt"
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

type entryEnricher func([]api.Entry) ([]api.Entry, error)

func streamEntries(ctx context.Context, sock string, pageSize int, build func(cursor string) ipc.Message, writer entryStreamWriter, enrich entryEnricher) error {
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
		entries := resp.Entries
		if enrich != nil {
			var err error
			entries, err = enrich(entries)
			if err != nil {
				return err
			}
		}
		if err := writer.WriteEntries(entries); err != nil {
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

func fetchFullEntries(ctx context.Context, sock string, entries []api.Entry) ([]api.Entry, error) {
	out := make([]api.Entry, 0, len(entries))
	for _, entry := range entries {
		resp, err := ipc.Request(ctx, sock, ipc.Message{
			Name:      "note.show",
			ID:        entry.ID,
			Namespace: entry.Namespace,
		})
		if err != nil {
			return nil, err
		}
		if !resp.OK || resp.Entry == nil {
			if resp.Msg != "" {
				return nil, fmt.Errorf("fetch %s: %s", entry.ID, resp.Msg)
			}
			return nil, fmt.Errorf("fetch %s: not found", entry.ID)
		}
		out = append(out, *resp.Entry)
	}
	return out, nil
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

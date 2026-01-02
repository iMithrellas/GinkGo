package cli

import (
	"context"
	"io"
	"os"
	"os/exec"

	"golang.org/x/term"

	"github.com/mithrel/ginkgo/internal/present"
	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/pkg/api"
)

const defaultPager = "less -FRSX"

type entryStreamWriter interface {
	WriteEntries([]api.Entry) error
	Close() error
}

func renderEntries(ctx context.Context, out, errOut io.Writer, entries []api.Entry, opts present.Options) error {
	if opts.Mode == present.ModeTUI {
		return present.RenderEntries(ctx, out, entries, opts)
	}
	return withPager(ctx, out, errOut, func(w io.Writer) error {
		return present.RenderEntries(ctx, w, entries, opts)
	})
}

func renderEntry(ctx context.Context, out, errOut io.Writer, entry api.Entry, opts present.Options) error {
	if opts.Mode == present.ModeTUI {
		return present.RenderEntry(ctx, out, entry, opts)
	}
	return withPager(ctx, out, errOut, func(w io.Writer) error {
		return present.RenderEntry(ctx, w, entry, opts)
	})
}

func withPager(ctx context.Context, out, errOut io.Writer, write func(io.Writer) error) error {
	outFile, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(outFile.Fd())) {
		return write(out)
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = defaultPager
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", pager)
	cmd.Stdout = outFile
	if errFile, ok := errOut.(*os.File); ok {
		cmd.Stderr = errFile
	} else {
		cmd.Stderr = os.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return write(out)
	}
	if err := cmd.Start(); err != nil {
		return write(out)
	}
	writeErr := write(stdin)
	_ = stdin.Close()
	waitErr := cmd.Wait()
	if writeErr != nil {
		return writeErr
	}
	return waitErr
}

func newEntryStreamWriter(w io.Writer, opts present.Options) entryStreamWriter {
	switch opts.Mode {
	case present.ModeJSON:
		return &jsonStreamWriter{jw: format.NewJSONStreamWriter(w, opts.JSONIndent)}
	case present.ModeNDJSON:
		return &ndjsonStreamWriter{nw: format.NewNDJSONStreamWriter(w)}
	case present.ModePretty:
		return &plainStreamWriter{pw: format.NewPlainStreamWriter(w, opts.Headers)}
	case present.ModePlain:
		return &plainStreamWriter{pw: format.NewPlainStreamWriter(w, opts.Headers)}
	default:
		return &plainStreamWriter{pw: format.NewPlainStreamWriter(w, opts.Headers)}
	}
}

type plainStreamWriter struct {
	pw *format.PlainStreamWriter
}

func (w *plainStreamWriter) WriteEntries(entries []api.Entry) error {
	return w.pw.WriteEntries(entries)
}

func (w *plainStreamWriter) Close() error {
	return w.pw.Close()
}

type jsonStreamWriter struct {
	jw *format.JSONStreamWriter
}

func (w *jsonStreamWriter) WriteEntries(entries []api.Entry) error {
	return w.jw.WriteEntries(entries)
}

func (w *jsonStreamWriter) Close() error {
	return w.jw.Close()
}

type ndjsonStreamWriter struct {
	nw *format.NDJSONStreamWriter
}

func (w *ndjsonStreamWriter) WriteEntries(entries []api.Entry) error {
	return w.nw.WriteEntries(entries)
}

func (w *ndjsonStreamWriter) Close() error {
	return w.nw.Close()
}

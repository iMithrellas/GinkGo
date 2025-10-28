package present

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/internal/present/tui"
	"github.com/mithrel/ginkgo/pkg/api"
)

type Mode int

const (
	ModePlain Mode = iota
	ModePretty
	ModeJSON
	ModeTUI
)

type Options struct {
	Mode            Mode
	JSONIndent      bool
	Headers         bool
	InitialStatus   string
	InitialDuration time.Duration
}

// ParseMode parses a string like "plain", "pretty", "json", "json+indent", "tui".
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "plain":
		return ModePlain, true
	case "pretty":
		return ModePretty, true
	case "json":
		return ModeJSON, true
	case "json+indent":
		return ModeJSON, true
	case "tui":
		return ModeTUI, true
	default:
		return ModePlain, false
	}
}

// RenderEntries renders a list of entries according to options.
func RenderEntries(ctx context.Context, w io.Writer, entries []api.Entry, opts Options) error {
	switch opts.Mode {
	case ModeJSON:
		return format.WriteJSONEntries(w, entries, opts.JSONIndent)
	case ModePlain:
		return format.WritePlainEntries(w, entries, opts.Headers)
	case ModePretty:
		// Pretty list currently falls back to plain list until glamour table is added.
		return format.WritePlainEntries(w, entries, opts.Headers)
	case ModeTUI:
		return tui.RenderTable(ctx, entries, opts.InitialStatus, opts.InitialDuration)
	default:
		return format.WritePlainEntries(w, entries, opts.Headers)
	}
}

// RenderEntry renders a single entry according to options.
func RenderEntry(ctx context.Context, w io.Writer, e api.Entry, opts Options) error {
	switch opts.Mode {
	case ModeJSON:
		return format.WriteJSONEntry(w, e, opts.JSONIndent)
	case ModePlain:
		return format.WritePlainEntry(w, e, opts.Headers)
	case ModePretty:
		return format.WritePrettyEntry(w, e)
	case ModeTUI:
		// Not applicable yet. Placeholder.
		return errors.New("tui output not supported for single entry yet")
	default:
		return format.WritePlainEntry(w, e, opts.Headers)
	}
}

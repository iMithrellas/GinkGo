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
	ModeNDJSON
	ModeTUI
)

type Options struct {
	Mode            Mode
	JSONIndent      bool
	Headers         bool
	InitialStatus   string
	InitialDuration time.Duration
	FilterTagsAny   string
	FilterTagsAll   string
	FilterSince     string
	FilterUntil     string
	Namespace       string
	TUIBufferRatio  float64
}

// ParseMode parses a string like "plain", "pretty", "json", "ndjson", "tui".
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "plain":
		return ModePlain, true
	case "pretty":
		return ModePretty, true
	case "json":
		return ModeJSON, true
	case "ndjson":
		return ModeNDJSON, true
	case "tui":
		return ModeTUI, true
	default:
		return ModeTUI, false
	}
}

// RenderEntries renders a list of entries according to options.
func RenderEntries(ctx context.Context, w io.Writer, entries []api.Entry, opts Options) error {
	switch opts.Mode {
	case ModeJSON:
		return format.WriteJSONEntries(w, entries, opts.JSONIndent)
	case ModeNDJSON:
		return format.WriteNDJSONEntries(w, entries)
	case ModePlain:
		return format.WritePlainEntries(w, entries, opts.Headers)
	case ModePretty:
		// Pretty list currently falls back to plain list until glamour table is added.
		return format.WritePlainEntries(w, entries, opts.Headers)
	case ModeTUI:
		// Pass headers flag through so the TUI can optionally hide column headers.
		return tui.RenderTable(ctx, entries, opts.Headers, opts.InitialStatus, opts.InitialDuration, opts.FilterTagsAny, opts.FilterTagsAll, opts.FilterSince, opts.FilterUntil, opts.Namespace, opts.TUIBufferRatio)
	default:
		return format.WritePlainEntries(w, entries, opts.Headers)
	}
}

// RenderEntry renders a single entry according to options.
func RenderEntry(ctx context.Context, w io.Writer, e api.Entry, opts Options) error {
	switch opts.Mode {
	case ModeJSON:
		return format.WriteJSONEntry(w, e, opts.JSONIndent)
	case ModeNDJSON:
		return format.WriteNDJSONEntry(w, e)
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

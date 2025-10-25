package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Main provides a standalone daemon entrypoint.
func Main() error {
	// HTTP health endpoint
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Addr: ":7465", Handler: mux}

	// IPC server on Unix socket
	sock, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Build app (store, logger) for handling requests
	cfg := config.DefaultConfig()
	app, err := wire.BuildApp(ctx, cfg)
	if err != nil {
		return err
	}
	go func() {
		_ = ipc.Serve(ctx, sock, func(m ipc.Message) ipc.Response {
			ns := m.Namespace
			if ns == "" {
				ns = app.Cfg.Namespace
			}
			switch m.Name {
			case "note.add", "note.edit":
				// Create if no ID, otherwise CAS update.
				now := time.Now().UTC()
				if m.ID == "" {
					e := api.Entry{ID: api.NewID(), Version: 1, Title: m.Title, Body: m.Body, Tags: m.Tags, CreatedAt: now, UpdatedAt: now, Namespace: ns}
					e, err := app.Store.Entries.CreateEntry(ctx, e)
					if err != nil {
						return ipc.Response{OK: false, Msg: err.Error()}
					}
					log.Printf("created note id=%s title=%q", e.ID, e.Title)
					return ipc.Response{OK: true, Entry: &e}
				}
				// Update path
				cur, err := app.Store.Entries.GetEntry(ctx, m.ID)
				if err != nil {
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				if cur.Namespace != ns {
					return ipc.Response{OK: false, Msg: "not found"}
				}
				if m.Title != "" {
					cur.Title = m.Title
				}
				if m.Body != "" {
					cur.Body = m.Body
				}
				if m.Tags != nil {
					cur.Tags = m.Tags
				}
				cur.UpdatedAt = now
				ifv := m.IfVersion
				if ifv == 0 {
					ifv = cur.Version
				}
				e, err := app.Store.Entries.UpdateEntryCAS(ctx, cur, ifv)
				if err != nil {
					if err == db.ErrConflict {
						latest, _ := app.Store.Entries.GetEntry(ctx, m.ID)
						return ipc.Response{OK: false, Msg: "conflict", Entry: &latest}
					}
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				log.Printf("updated note id=%s title=%q", e.ID, e.Title)
				return ipc.Response{OK: true, Entry: &e}
			case "note.delete":
				if m.ID == "" {
					return ipc.Response{OK: false, Msg: "missing id"}
				}
				if err := app.Store.Entries.DeleteEntry(ctx, m.ID); err != nil {
					log.Printf("delete note id=%s err=%v", m.ID, err)
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				log.Printf("deleted note id=%s", m.ID)
				return ipc.Response{OK: true}
			case "note.show":
				if m.ID == "" {
					return ipc.Response{OK: false, Msg: "missing id"}
				}
				e, err := app.Store.Entries.GetEntry(ctx, m.ID)
				if err != nil {
					log.Printf("show note id=%s err=%v", m.ID, err)
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				if e.Namespace != ns {
					return ipc.Response{OK: false, Msg: "not found"}
				}
				log.Printf("show note id=%s", m.ID)
				return ipc.Response{OK: true, Entry: &e}
			case "note.list":
				log.Printf("list notes")
				entries, _, err := app.Store.Entries.ListEntries(ctx, api.ListQuery{Namespace: ns})
				if err != nil {
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				log.Printf("list notes count=%d", len(entries))
				return ipc.Response{OK: true, Entries: entries}
			case "note.search.fts":
				q := strings.ToLower(strings.TrimSpace(m.Title))
				entries, _, err := app.Store.Entries.Search(ctx, api.SearchQuery{Namespace: ns, Query: q, Regex: false})
				if err != nil {
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				return ipc.Response{OK: true, Entries: entries}
			case "note.search.regex":
				pattern := m.Title
				if _, err := regexp.Compile(pattern); err != nil {
					return ipc.Response{OK: false, Msg: "bad regex"}
				}
				entries, _, err := app.Store.Entries.Search(ctx, api.SearchQuery{Namespace: ns, Query: pattern, Regex: true})
				if err != nil {
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				return ipc.Response{OK: true, Entries: entries}
			default:
				log.Printf("unknown IPC cmd=%s", m.Name)
				return ipc.Response{OK: false, Msg: "unknown command"}
			}
		})
	}()

	// Run HTTP server (optional for now). Shut down IPC when HTTP stops.
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	err = srv.ListenAndServe()
	cancel()
	// Allow some time for IPC goroutine to exit cleanly
	time.Sleep(100 * time.Millisecond)
	return err
}

// Start launches the HTTP server on a provided listener (used by tests or CLI control).
func Start(ctx context.Context, l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	return srv.Serve(l)
}

// longestWord returns the longest alphanumeric token (>=3 chars) in s, lowercased.
func longestWord(s string) string {
	s = strings.ToLower(s)
	best := ""
	run := []rune{}
	flush := func() {
		if len(run) >= 3 {
			w := string(run)
			if len(w) > len(best) {
				best = w
			}
		}
		run = run[:0]
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return best
}

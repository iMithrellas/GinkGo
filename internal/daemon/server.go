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

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Run starts the daemon using the provided, already-wired App (config, store, logger).
// The caller controls the lifecycle via ctx.
func Run(ctx context.Context, app *wire.App) error {
	// HTTP health endpoint
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	addr := app.Cfg.GetString("http_addr")
	if strings.TrimSpace(addr) == "" {
		addr = ":7465"
	}
	srv := &http.Server{Addr: addr, Handler: mux}

	// IPC server on Unix socket using protobuf transport
	sock, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Start continuous background sync loop
	go app.Syncer.RunBackground(ctx)
	// Adapt CLI message handler to protobuf transport
	handler := ipc.PBHandler(func(m ipc.Message) ipc.Response {
		ns := m.Namespace
		if ns == "" {
			ns = app.Cfg.GetString("namespace")
		}
		switch m.Name {
		case "note.add", "note.edit":
			// Create if no ID, otherwise CAS update.
			now := time.Now().UTC()
			if m.ID == "" {
				tags := normalizeTags(m.Tags)
				e := api.Entry{ID: api.NewID(), Version: 1, Title: m.Title, Body: m.Body, Tags: tags, CreatedAt: now, UpdatedAt: now, Namespace: ns}
				e, err := app.Store.Entries.CreateEntry(ctx, e)
				if err != nil {
					return ipc.Response{OK: false, Msg: err.Error()}
				}
				log.Printf("created note id=%s title=%q", e.ID, e.Title)
				go app.Syncer.SyncNow(ctx)
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
				cur.Tags = normalizeTags(m.Tags)
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
			go app.Syncer.SyncNow(ctx)
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
			go app.Syncer.SyncNow(ctx)
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
			var entries []api.Entry
			var err error
			since, until := parseBounds(m.Since, m.Until)
			entries, _, err = app.Store.Entries.ListEntries(ctx, api.ListQuery{
				Namespace: ns,
				Any:       m.TagsAny,
				All:       m.TagsAll,
				Since:     since,
				Until:     until,
			})
			if err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			log.Printf("list notes count=%d", len(entries))
			return ipc.Response{OK: true, Entries: entries}
		case "note.search.fts":
			q := strings.ToLower(strings.TrimSpace(m.Title))
			since, until := parseBounds(m.Since, m.Until)
			entries, _, err := app.Store.Entries.Search(ctx, api.SearchQuery{Namespace: ns, Query: q, Regex: false, Any: m.TagsAny, All: m.TagsAll, Since: since, Until: until})
			if err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			return ipc.Response{OK: true, Entries: entries}
		case "note.search.regex":
			pattern := m.Title
			since, until := parseBounds(m.Since, m.Until)
			if _, err := regexp.Compile(pattern); err != nil {
				return ipc.Response{OK: false, Msg: "bad regex"}
			}
			entries, _, err := app.Store.Entries.Search(ctx, api.SearchQuery{Namespace: ns, Query: pattern, Regex: true, Any: m.TagsAny, All: m.TagsAll, Since: since, Until: until})
			if err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			return ipc.Response{OK: true, Entries: entries}
		case "sync.queue":
			limit := m.Limit
			qs, err := app.Syncer.Queue(ctx, limit, m.Remote)
			if err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			out := make([]ipc.QueueRemote, 0, len(qs))
			for _, qr := range qs {
				r := ipc.QueueRemote{Name: qr.Name, URL: qr.URL, Pending: int64(qr.Pending)}
				if len(qr.Events) > 0 {
					r.Events = make([]ipc.QueueEvent, 0, len(qr.Events))
					for _, ev := range qr.Events {
						r.Events = append(r.Events, ipc.QueueEvent{Time: ev.Time, Type: ev.Type, ID: ev.ID})
					}
				}
				out = append(out, r)
			}
			return ipc.Response{OK: true, Queue: out}
		case "sync.run":
			if err := app.Syncer.SyncNow(ctx); err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			return ipc.Response{OK: true, Msg: "sync triggered"}
		case "namespace.list":
			nss, err := app.Store.Entries.ListNamespaces(ctx)
			if err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			return ipc.Response{OK: true, Namespaces: nss}
		case "tag.list":
			tags, err := app.Store.Entries.ListTags(ctx, api.TagsQuery{Namespace: ns})
			if err != nil {
				return ipc.Response{OK: false, Msg: err.Error()}
			}
			return ipc.Response{OK: true, Tags: tags}
		default:
			log.Printf("unknown IPC cmd=%s", m.Name)
			return ipc.Response{OK: false, Msg: "unknown command"}
		}
	})
	go func() {
		srv := transport.NewUnixServer(transport.UnixListener{Path: sock})
		_ = srv.Serve(ctx, handler)
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

// normalizeTags lowercases and trims tags, removing empties and duplicates while
// preserving first-seen order.
func normalizeTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		tt := strings.ToLower(strings.TrimSpace(t))
		if tt == "" {
			continue
		}
		if _, ok := seen[tt]; ok {
			continue
		}
		seen[tt] = struct{}{}
		out = append(out, tt)
	}
	return out
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

// parseBounds parses RFC3339 time strings, returns zero values when parsing fails.
func parseBounds(since, until string) (time.Time, time.Time) {
	var s, u time.Time
	if ts := strings.TrimSpace(since); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			s = t.UTC()
		}
	}
	if ts := strings.TrimSpace(until); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			u = t.UTC()
		}
	}
	return s, u
}

package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/db"
	pbmsg "github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Server serves HTTP replication endpoints backed by a Store.
type Server struct {
	cfg   *viper.Viper
	store *db.Store
}

func New(cfg *viper.Viper, store *db.Store) *Server {
	return &Server{cfg: cfg, store: store}
}

// Router returns an http.Handler with registered routes.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/replicate/push", s.auth(s.handlePush))
	mux.HandleFunc("/v1/replicate/pull", s.auth(s.handlePull))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimSpace(s.cfg.GetString("auth.token"))
		got := r.Header.Get("Authorization")
		if tok == "" || !strings.HasPrefix(got, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(got, "Bearer ")) != tok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var batch pbmsg.PushBatch
	if err := proto.Unmarshal(b, &batch); err != nil {
		http.Error(w, "bad protobuf", http.StatusBadRequest)
		return
	}
	out := make([]*pbmsg.ItemStatus, 0, len(batch.Events))
	var last time.Time
	for _, pev := range batch.Events {
		st := &pbmsg.ItemStatus{Id: pev.GetId(), Ok: true}
		if t := pev.GetTime(); t != nil {
			tt := t.AsTime()
			if tt.After(last) {
				last = tt
			}
		}
		switch strings.ToLower(pev.GetType()) {
		case string(api.EventUpsert):
			if pev.Entry == nil {
				st.Ok = false
				st.Msg = "missing entry"
			} else if err := s.applyUpsert(r.Context(), fromPbEntry(pev.Entry)); err != nil {
				st.Ok = false
				st.Msg = err.Error()
			}
		case string(api.EventDelete):
			if err := s.store.Entries.DeleteEntry(r.Context(), pev.GetId()); err != nil {
				st.Ok = false
				st.Msg = err.Error()
			}
		default:
			st.Ok = false
			st.Msg = "unknown event"
		}
		out = append(out, st)
	}
	resp := &pbmsg.PushResult{Items: out, Next: &pbmsg.Cursor{After: timestamppb.New(last)}}
	w.Header().Set("Content-Type", "application/x-protobuf")
	enc, _ := proto.Marshal(resp)
	_, _ = w.Write(enc)
}

func (s *Server) applyUpsert(ctx context.Context, e api.Entry) error {
	// Try create; on conflict, fetch and CAS update
	_, err := s.store.Entries.CreateEntry(ctx, e)
	if err == nil {
		log.Printf("replicate: created id=%s", e.ID)
		return nil
	}
	if !errors.Is(err, db.ErrConflict) {
		return err
	}
	cur, err := s.store.Entries.GetEntry(ctx, e.ID)
	if err != nil {
		return err
	}
	// advance to the incoming version; CAS on current version
	_, err = s.store.Entries.UpdateEntryCAS(ctx, e, cur.Version)
	if err != nil {
		return err
	}
	log.Printf("replicate: updated id=%s", e.ID)
	return nil
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	var after time.Time
	if a := strings.TrimSpace(q.Get("after")); a != "" {
		if t, err := time.Parse(time.RFC3339Nano, a); err == nil {
			after = t
		} else if t, err := time.Parse(time.RFC3339, a); err == nil {
			after = t
		} else {
			http.Error(w, "bad after", http.StatusBadRequest)
			return
		}
	}
	limit := 256
	if ls := strings.TrimSpace(q.Get("limit")); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			limit = n
		}
	}
	evs, nextCur, err := s.store.Events.List(r.Context(), api.Cursor{After: after}, limit)
	if err != nil {
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	out := make([]*pbmsg.RepEvent, 0, len(evs))
	for _, e := range evs {
		var pe *pbmsg.Entry
		if e.Entry != nil {
			pe = &pbmsg.Entry{
				Id:        e.Entry.ID,
				Version:   e.Entry.Version,
				Title:     e.Entry.Title,
				Body:      e.Entry.Body,
				Tags:      append([]string(nil), e.Entry.Tags...),
				CreatedAt: timestamppb.New(e.Entry.CreatedAt),
				UpdatedAt: timestamppb.New(e.Entry.UpdatedAt),
				Namespace: e.Entry.Namespace,
			}
		}
		out = append(out, &pbmsg.RepEvent{
			Time:  timestamppb.New(e.Time),
			Type:  string(e.Type),
			Id:    e.ID,
			Entry: pe,
		})
	}
	resp := &pbmsg.PullResult{Events: out}
	if !nextCur.After.IsZero() {
		resp.Next = &pbmsg.Cursor{After: timestamppb.New(nextCur.After)}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	b, _ := proto.Marshal(resp)
	_, _ = w.Write(b)
}

func fromPbEntry(in *pbmsg.Entry) api.Entry {
	var c, u time.Time
	if in.GetCreatedAt() != nil {
		c = in.GetCreatedAt().AsTime()
	}
	if in.GetUpdatedAt() != nil {
		u = in.GetUpdatedAt().AsTime()
	}
	return api.Entry{
		ID:        in.GetId(),
		Version:   in.GetVersion(),
		Title:     in.GetTitle(),
		Body:      in.GetBody(),
		Tags:      append([]string(nil), in.GetTags()...),
		CreatedAt: c,
		UpdatedAt: u,
		Namespace: in.GetNamespace(),
	}
}

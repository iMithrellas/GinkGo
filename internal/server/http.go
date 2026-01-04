package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	gcrypto "github.com/mithrel/ginkgo/internal/crypto"
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
		var evTime time.Time
		if t := pev.GetTime(); t != nil {
			evTime = t.AsTime()
			if evTime.After(last) {
				last = evTime
			}
		}
		evType := api.EventType(strings.ToLower(pev.GetType()))
		if pev.GetPayloadType() == "" || len(pev.GetPayload()) == 0 {
			st.Ok = false
			st.Msg = "missing payload"
			out = append(out, st)
			continue
		}
		if err := s.verifyRepEventSignature(pev); err != nil {
			st.Ok = false
			st.Msg = err.Error()
			out = append(out, st)
			continue
		}
		ev := api.Event{
			Time:        evTime,
			Type:        evType,
			ID:          pev.GetId(),
			Namespace:   pev.GetNamespaceId(),
			PayloadType: pev.GetPayloadType(),
			Payload:     append([]byte(nil), pev.GetPayload()...),
			OriginLabel: pev.GetOriginLabel(),
			SignerID:    pev.GetSignerId(),
			Sig:         append([]byte(nil), pev.GetSig()...),
		}
		if err := s.store.Events.Append(r.Context(), ev); err != nil {
			st.Ok = false
			st.Msg = err.Error()
		}
		out = append(out, st)
	}
	resp := &pbmsg.PushResult{Items: out, Next: &pbmsg.Cursor{After: timestamppb.New(last)}}
	w.Header().Set("Content-Type", "application/x-protobuf")
	enc, _ := proto.Marshal(resp)
	_, _ = w.Write(enc)
}

func (s *Server) verifyRepEventSignature(pev *pbmsg.RepEvent) error {
	ns := strings.TrimSpace(pev.GetNamespaceId())
	trusted, err := s.trustedSigners(ns)
	if err != nil {
		return err
	}
	if len(trusted) == 0 {
		return nil
	}
	if len(pev.GetSig()) == 0 || strings.TrimSpace(pev.GetSignerId()) == "" {
		return fmt.Errorf("missing signature")
	}
	if pev.GetTime() == nil {
		return fmt.Errorf("missing time")
	}
	signBytes, err := gcrypto.SignPayload(
		1,
		pev.GetTime().AsTime().UnixNano(),
		strings.ToLower(pev.GetType()),
		pev.GetId(),
		ns,
		pev.GetPayloadType(),
		pev.GetOriginLabel(),
		pev.GetPayload(),
	)
	if err != nil {
		return err
	}
	pub, ok := trusted[pev.GetSignerId()]
	if !ok {
		return fmt.Errorf("untrusted signer")
	}
	if err := gcrypto.VerifyEvent(pub, signBytes, pev.GetSig()); err != nil {
		return err
	}
	return nil
}

func (s *Server) trustedSigners(ns string) (map[string]ed25519.PublicKey, error) {
	base := "namespaces." + ns + ".trusted_signers"
	values := s.cfg.GetStringSlice(base)
	if len(values) == 0 {
		if raw := strings.TrimSpace(s.cfg.GetString(base)); raw != "" {
			values = strings.Split(raw, ",")
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]ed25519.PublicKey, len(values))
	for _, v := range values {
		trim := strings.TrimSpace(v)
		if trim == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(trim)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted signer key")
		}
		if len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid trusted signer key length")
		}
		id := gcrypto.SignerID(ed25519.PublicKey(b))
		out[id] = ed25519.PublicKey(b)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
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
		out = append(out, &pbmsg.RepEvent{
			Time:        timestamppb.New(e.Time),
			Type:        string(e.Type),
			Id:          e.ID,
			NamespaceId: e.Namespace,
			PayloadType: e.PayloadType,
			Payload:     e.Payload,
			OriginLabel: e.OriginLabel,
			SignerId:    e.SignerID,
			Sig:         e.Sig,
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

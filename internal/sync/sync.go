package sync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"

	"golang.org/x/crypto/chacha20poly1305"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	gcrypto "github.com/mithrel/ginkgo/internal/crypto"
	"github.com/mithrel/ginkgo/internal/db"
	pbmsg "github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/internal/keys"
	"github.com/mithrel/ginkgo/pkg/api"
)

type Service struct {
	cfg        *viper.Viper
	store      *db.Store
	httpClient *http.Client
}

type remoteConfig struct {
	Name      string
	URL       string
	Token     string
	BatchSize int
}

func New(cfg *viper.Viper, store *db.Store) *Service {
	return &Service{
		cfg:   cfg,
		store: store,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (s *Service) remoteEnabled(name string) bool {
	base := "remotes." + name + "."
	url := strings.TrimSpace(s.cfg.GetString(base + "url"))
	enabled := s.cfg.GetBool(base + "enabled")
	if s.cfg.IsSet(base + "enabled") {
		return enabled && url != ""
	}
	return url != ""
}

func (s *Service) SyncNow(ctx context.Context) error {
	remotes := s.cfg.GetStringMap("remotes")
	if len(remotes) == 0 {
		return nil
	}
	var firstErr error

	for name := range remotes {
		if !s.remoteEnabled(name) {
			continue
		}

		// Load config once per remote
		rc, err := s.getRemoteConfig(name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		pushAfter, pullAfter := s.loadCursors(name)
		log.Printf("sync: %s starting. pushAfter=%v pullAfter=%v", name, pushAfter, pullAfter)

		if err := s.pushRemote(ctx, rc, pushAfter); err != nil {
			log.Printf("sync: %s push failed: %v", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		if err := s.pullRemote(ctx, rc, pullAfter); err != nil {
			log.Printf("sync: %s pull failed: %v", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *Service) getRemoteConfig(name string) (remoteConfig, error) {
	base := "remotes." + name + "."
	u := strings.TrimRight(s.cfg.GetString(base+"url"), "/")
	token := s.cfg.GetString(base + "token")

	if u == "" {
		return remoteConfig{}, fmt.Errorf("remote %s missing url", name)
	}
	if strings.TrimSpace(token) == "" {
		return remoteConfig{}, fmt.Errorf("remote %s missing token", name)
	}

	batchSize := s.cfg.GetInt("sync.batch_size")
	if batchSize <= 0 {
		batchSize = 256
	}

	return remoteConfig{
		Name:      name,
		URL:       u,
		Token:     token,
		BatchSize: batchSize,
	}, nil
}

func (s *Service) execRequest(ctx context.Context, method, url, token, contentType string, body []byte) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, 0, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func (s *Service) pushRemote(ctx context.Context, rc remoteConfig, pushAfter time.Time) error {
	evs, nextCur, err := s.store.Events.List(ctx, api.Cursor{After: pushAfter}, rc.BatchSize)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	if len(evs) == 0 {
		return nil
	}

	pbBatch, err := s.eventsToProto(evs)
	if err != nil {
		return err
	}
	body, _ := proto.Marshal(pbBatch)

	respBody, code, err := s.execRequest(ctx, http.MethodPost, rc.URL+"/v1/replicate/push", rc.Token, "application/x-protobuf", body)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("remote %s push failed: %s", rc.Name, strings.TrimSpace(string(respBody)))
	}

	newPush := nextCur.After
	if newPush.IsZero() && len(evs) > 0 {
		newPush = evs[len(evs)-1].Time
	}
	s.savePushAfter(rc.Name, newPush)
	return nil
}

func (s *Service) pullRemote(ctx context.Context, rc remoteConfig, pullAfter time.Time) error {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(rc.BatchSize))
	if !pullAfter.IsZero() {
		q.Set("after", pullAfter.UTC().Format(time.RFC3339Nano))
	}

	pullURL := rc.URL + "/v1/replicate/pull?" + q.Encode()
	log.Printf("sync: pulling %s", pullURL)

	respBody, code, err := s.execRequest(ctx, http.MethodGet, pullURL, rc.Token, "", nil)
	if err != nil {
		return err
	}

	if code == http.StatusNotImplemented {
		return nil
	}
	if code >= 300 {
		return fmt.Errorf("remote %s pull failed: %s", rc.Name, strings.TrimSpace(string(respBody)))
	}

	var pr pbmsg.PullResult
	if err := proto.Unmarshal(respBody, &pr); err != nil {
		return err
	}
	log.Printf("sync: pulled %d events from %s", len(pr.Events), rc.Name)
	if len(pr.Events) == 0 {
		return nil
	}
	if err := s.applyPullBatch(ctx, pr.Events); err != nil {
		return err
	}

	// Determine next cursor
	cur := pullAfter
	if pr.Next != nil && pr.Next.After != nil {
		cur = pr.Next.After.AsTime()
	} else if len(pr.Events) > 0 {
		if t := pr.Events[len(pr.Events)-1].GetTime(); t != nil {
			cur = t.AsTime()
		}
	}

	if !cur.IsZero() {
		s.savePullAfter(rc.Name, cur)
	}
	return nil
}

// eventsToProto handles verbose mapping logic
func (s *Service) eventsToProto(evs []api.Event) (*pbmsg.PushBatch, error) {
	pbBatch := &pbmsg.PushBatch{Events: make([]*pbmsg.RepEvent, 0, len(evs))}
	signerCache := map[string]*signerInfo{}
	originCache := map[string]string{}
	for _, e := range evs {
		ns := e.Namespace
		if ns == "" && e.Entry != nil {
			ns = e.Entry.Namespace
		}
		payloadType := e.PayloadType
		payload := e.Payload
		if payloadType == "" || len(payload) == 0 {
			var err error
			payloadType, payload, err = encodePlainPayload(e, ns)
			if err != nil {
				return nil, err
			}
		}
		if s.e2eeEnabled(ns) && payloadType == "plain_v1" {
			var err error
			payloadType, payload, err = s.encryptPayload(ns, payload)
			if err != nil {
				return nil, err
			}
		}
		origin := originCache[ns]
		if origin == "" {
			origin = s.originLabel(ns)
			originCache[ns] = origin
		}

		var signerID string
		var sig []byte
		if signerCache[ns] == nil {
			info, err := s.signerForNamespace(ns)
			if err != nil {
				return nil, err
			}
			signerCache[ns] = info
		}
		if signerCache[ns] != nil {
			info := signerCache[ns]
			signBytes, err := gcrypto.SignPayload(1, e.Time.UnixNano(), string(e.Type), e.ID, ns, payloadType, origin, payload)
			if err != nil {
				return nil, err
			}
			sig, err = gcrypto.SignEvent(info.Priv, signBytes)
			if err != nil {
				return nil, err
			}
			signerID = info.ID
		}
		pbBatch.Events = append(pbBatch.Events, &pbmsg.RepEvent{
			Time:        timestamppb.New(e.Time),
			Type:        string(e.Type),
			Id:          e.ID,
			NamespaceId: ns,
			PayloadType: payloadType,
			Payload:     payload,
			SignerId:    signerID,
			Sig:         sig,
			OriginLabel: origin,
		})
	}
	return pbBatch, nil
}

// repEventToAPI converts a pulled RepEvent into a local api.Event.
func (s *Service) repEventToAPI(pev *pbmsg.RepEvent) (api.Event, error) {
	ev := api.Event{
		ID:          pev.GetId(),
		Type:        api.EventType(strings.ToLower(pev.GetType())),
		Namespace:   pev.GetNamespaceId(),
		PayloadType: pev.GetPayloadType(),
		Payload:     append([]byte(nil), pev.GetPayload()...),
		OriginLabel: pev.GetOriginLabel(),
		SignerID:    pev.GetSignerId(),
		Sig:         append([]byte(nil), pev.GetSig()...),
	}
	if pev.GetTime() != nil {
		ev.Time = pev.GetTime().AsTime()
	}
	switch ev.PayloadType {
	case "plain_v1":
		if len(ev.Payload) > 0 {
			entry, ns, err := decodePlainPayload(ev.Type, ev.Payload)
			if err != nil {
				return api.Event{}, err
			}
			ev.Entry = entry
			if ev.Namespace == "" {
				ev.Namespace = ns
			}
		}
	case "enc_v1":
		if len(ev.Payload) > 0 {
			plain, err := s.decryptPayload(ev.Namespace, ev.Payload)
			if err != nil {
				return api.Event{}, err
			}
			entry, ns, err := decodePlainPayload(ev.Type, plain)
			if err != nil {
				return api.Event{}, err
			}
			ev.Entry = entry
			if ev.Namespace == "" {
				ev.Namespace = ns
			}
		}
	}
	return ev, nil
}

// applyPullBatch applies pulled events without re-logging them locally.
func (s *Service) applyPullBatch(ctx context.Context, in []*pbmsg.RepEvent) error {
	evs := make([]api.Event, 0, len(in))
	for _, pev := range in {
		ev, err := s.repEventToAPI(pev)
		if err != nil {
			return err
		}
		evs = append(evs, ev)
	}
	if err := s.store.ApplyReplicationBatch(ctx, evs); err != nil && err != db.ErrConflict && err != db.ErrNotFound {
		return err
	}
	return nil
}

func encodePlainPayload(ev api.Event, ns string) (string, []byte, error) {
	switch ev.Type {
	case api.EventUpsert:
		if ev.Entry == nil {
			return "", nil, fmt.Errorf("plain_v1 upsert requires entry")
		}
		b, err := json.Marshal(ev.Entry)
		return "plain_v1", b, err
	case api.EventDelete:
		dp := struct {
			ID        string `json:"id"`
			Namespace string `json:"namespace"`
		}{ID: ev.ID, Namespace: ns}
		b, err := json.Marshal(dp)
		return "plain_v1", b, err
	default:
		return "", nil, fmt.Errorf("unknown event type %q", ev.Type)
	}
}

func decodePlainPayload(evType api.EventType, payload []byte) (*api.Entry, string, error) {
	switch evType {
	case api.EventUpsert:
		var e api.Entry
		if err := json.Unmarshal(payload, &e); err != nil {
			return nil, "", err
		}
		return &e, e.Namespace, nil
	case api.EventDelete:
		var dp struct {
			ID        string `json:"id"`
			Namespace string `json:"namespace"`
		}
		if err := json.Unmarshal(payload, &dp); err != nil {
			return nil, "", err
		}
		return nil, dp.Namespace, nil
	default:
		return nil, "", fmt.Errorf("unknown event type %q", evType)
	}
}

type encryptedPayloadV1 struct {
	Version    uint8  `json:"version"`
	KeyID      string `json:"key_id,omitempty"`
	Alg        string `json:"alg"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func (s *Service) encryptPayload(ns string, payload []byte) (string, []byte, error) {
	key, keyID, err := s.keyForNamespace(ns, "write")
	if err != nil {
		return "", nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	ciphertext := aead.Seal(nil, nonce, payload, nil)
	env := encryptedPayloadV1{
		Version:    1,
		KeyID:      keyID,
		Alg:        "xchacha20poly1305",
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return "", nil, err
	}
	return "enc_v1", b, nil
}

func (s *Service) decryptPayload(ns string, payload []byte) ([]byte, error) {
	var env encryptedPayloadV1
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	if env.Version != 1 {
		return nil, fmt.Errorf("unsupported enc_v1 version %d", env.Version)
	}
	if env.Alg != "xchacha20poly1305" {
		return nil, fmt.Errorf("unsupported enc_v1 alg %s", env.Alg)
	}
	key, _, err := s.keyForNamespace(ns, "read")
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("invalid enc_v1 nonce length")
	}
	plain, err := aead.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

type signerInfo struct {
	ID     string
	Priv   ed25519.PrivateKey
	Origin string
}

func (s *Service) e2eeEnabled(ns string) bool {
	if strings.TrimSpace(ns) == "" {
		return false
	}
	return s.cfg.GetBool("namespaces." + ns + ".e2ee")
}

func (s *Service) keyForNamespace(ns, kind string) ([]byte, string, error) {
	if strings.TrimSpace(ns) == "" {
		return nil, "", fmt.Errorf("namespace is required")
	}
	base := "namespaces." + ns + "."
	provider := strings.TrimSpace(s.cfg.GetString(base + "key_provider"))
	if provider == "" {
		provider = "config"
	}
	keyID := strings.TrimSpace(s.cfg.GetString(base + "key_id"))
	switch provider {
	case "system":
		if keyID == "" {
			return nil, "", fmt.Errorf("namespace %s missing key_id", ns)
		}
		ks := &keys.KeyringStore{}
		key, err := ks.Get(keyID + "/" + kind)
		if err != nil {
			return nil, "", err
		}
		return key, keyID, nil
	case "config":
		var b64 string
		if kind == "read" {
			b64 = strings.TrimSpace(s.cfg.GetString(base + "read_key"))
		} else {
			b64 = strings.TrimSpace(s.cfg.GetString(base + "write_key"))
		}
		if b64 == "" {
			return nil, "", fmt.Errorf("namespace %s missing %s_key", ns, kind)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, "", err
		}
		return key, keyID, nil
	default:
		return nil, "", fmt.Errorf("unsupported key_provider %q for namespace %s", provider, ns)
	}
}

func (s *Service) originLabel(ns string) string {
	base := "namespaces." + ns + ".origin_label"
	if v := strings.TrimSpace(s.cfg.GetString(base)); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "ginkgo-device"
	}
	return host
}

func (s *Service) signerForNamespace(ns string) (*signerInfo, error) {
	base := "namespaces." + ns + "."
	provider := strings.TrimSpace(s.cfg.GetString(base + "signer_key_provider"))
	if provider == "" {
		return nil, nil
	}
	switch provider {
	case "system":
		keyID := strings.TrimSpace(s.cfg.GetString(base + "signer_key_id"))
		if keyID == "" {
			return nil, fmt.Errorf("namespace %s missing signer_key_id", ns)
		}
		ks := &keys.KeyringStore{}
		privBytes, err := ks.Get(keyID + "/priv")
		if err != nil {
			return nil, err
		}
		priv := normalizePrivKey(privBytes)
		if priv == nil {
			return nil, fmt.Errorf("invalid signer private key")
		}
		pub := derivePubFromPriv(priv)
		if pub == nil {
			return nil, fmt.Errorf("invalid signer public key")
		}
		id := gcrypto.SignerID(pub)
		return &signerInfo{ID: id, Priv: priv}, nil
	case "config":
		privB64 := strings.TrimSpace(s.cfg.GetString(base + "signer_priv"))
		if privB64 == "" {
			return nil, fmt.Errorf("namespace %s missing signer_priv", ns)
		}
		privBytes, err := decodeKeyB64(privB64)
		if err != nil {
			return nil, err
		}
		priv := normalizePrivKey(privBytes)
		if priv == nil {
			return nil, fmt.Errorf("invalid signer private key")
		}
		pubB64 := strings.TrimSpace(s.cfg.GetString(base + "signer_pub"))
		var pub []byte
		if pubB64 != "" {
			pub, err = decodeKeyB64(pubB64)
			if err != nil {
				return nil, err
			}
		} else {
			pub = derivePubFromPriv(priv)
		}
		if pub == nil {
			return nil, fmt.Errorf("invalid signer public key")
		}
		id := gcrypto.SignerID(pub)
		return &signerInfo{ID: id, Priv: priv}, nil
	default:
		return nil, fmt.Errorf("unsupported signer_key_provider %q for namespace %s", provider, ns)
	}
}

func decodeKeyB64(v string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 key")
	}
	return b, nil
}

func normalizePrivKey(priv []byte) ed25519.PrivateKey {
	if len(priv) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(priv)
	}
	if len(priv) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(priv)
	}
	return nil
}

func derivePubFromPriv(priv ed25519.PrivateKey) []byte {
	if len(priv) == ed25519.SeedSize {
		priv = ed25519.NewKeyFromSeed(priv)
	}
	if len(priv) == ed25519.PrivateKeySize {
		return priv.Public().(ed25519.PublicKey)
	}
	return nil
}

func (s *Service) cursorPath(name string) string {
	dir := s.cfg.GetString("data_dir")
	p := filepath.Join(dir, "sync")
	_ = os.MkdirAll(p, 0o700)
	return filepath.Join(p, "cursor_"+name+".json")
}

type cursorsFile struct {
	PushAfter string `json:"push_after"`
	PullAfter string `json:"pull_after"`
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func (s *Service) loadCursors(name string) (time.Time, time.Time) {
	p := s.cursorPath(name)
	b, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	var cf cursorsFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return time.Time{}, time.Time{}
	}
	push := parseTS(cf.PushAfter)
	pull := parseTS(cf.PullAfter)
	return push, pull
}

func (s *Service) savePushAfter(name string, t time.Time) {
	if t.IsZero() {
		return
	}
	p := s.cursorPath(name)
	var cf cursorsFile
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &cf)
	}
	cf.PushAfter = t.UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(cf)
	_ = os.WriteFile(p, b, 0o600)
}
func (s *Service) savePullAfter(name string, t time.Time) {
	if t.IsZero() {
		return
	}
	p := s.cursorPath(name)
	var cf cursorsFile
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &cf)
	}
	cf.PullAfter = t.UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(cf)
	_ = os.WriteFile(p, b, 0o600)
}

func (s *Service) RunBackground(ctx context.Context) {
	base := s.cfg.GetDuration("sync.interval")
	if base == 0 {
		base = 60 * time.Second
	}
	fib := func() func() int {
		a, b := 1, 1
		return func() int { a, b = b, a+b; return a }
	}()
	next := base
	for {
		if err := s.SyncNow(ctx); err != nil {
			step := fib()
			max := s.cfg.GetDuration("sync.max_backoff")
			if max == 0 {
				max = 10 * time.Minute
			}
			next = base * time.Duration(step)
			if next > max {
				next = max
			}
		} else {
			next = base
			fib = func() func() int { a, b := 1, 1; return func() int { a, b = b, a+b; return a } }()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

type qEvent struct {
	Time time.Time
	Type string
	ID   string
}

type qRemote struct {
	Name    string
	URL     string
	Pending int
	Events  []qEvent
}

func (s *Service) Queue(ctx context.Context, limit int, onlyRemote string) ([]qRemote, error) {
	if limit <= 0 {
		limit = 10
	}
	remotes := s.cfg.GetStringMap("remotes")
	out := make([]qRemote, 0, len(remotes))
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if onlyRemote != "" && name != onlyRemote {
			continue
		}
		if !s.remoteEnabled(name) {
			continue
		}
		base := "remotes." + name + "."
		url := strings.TrimSpace(s.cfg.GetString(base + "url"))
		pushAfter, _ := s.loadCursors(name)
		total := 0
		const page = 500
		cur := api.Cursor{After: pushAfter}
		var sample []qEvent
		for {
			evs, next, err := s.store.Events.List(ctx, cur, page)
			if err != nil {
				return nil, err
			}
			total += len(evs)
			if len(sample) == 0 && len(evs) > 0 {
				// take the first 'limit' items from the head
				n := limit
				if n > len(evs) {
					n = len(evs)
				}
				sample = make([]qEvent, 0, n)
				for i := 0; i < n; i++ {
					sample = append(sample, qEvent{Time: evs[i].Time, Type: string(evs[i].Type), ID: evs[i].ID})
				}
			}
			if len(evs) < page {
				break
			}
			cur = next
		}
		out = append(out, qRemote{Name: name, URL: url, Pending: total, Events: sample})
	}
	return out, nil
}

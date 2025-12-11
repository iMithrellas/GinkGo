package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/db"
	pbmsg "github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/pkg/api"
)

type Service struct {
	cfg        *viper.Viper
	store      *db.Store
	httpClient *http.Client
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
		if err := s.pushRemote(ctx, name); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
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

func (s *Service) pushRemote(ctx context.Context, name string) error {
	base := "remotes." + name + "."
	url := strings.TrimRight(s.cfg.GetString(base+"url"), "/")
	token := s.cfg.GetString(base + "token")
	if url == "" {
		return fmt.Errorf("remote %s missing url", name)
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("remote %s missing token", name)
	}
	pushAfter, pullAfter := s.loadCursors(name)
	batchSize := s.cfg.GetInt("sync.batch_size")
	if batchSize <= 0 {
		batchSize = 256
	}
	evs, nextCur, err := s.store.Events.List(ctx, api.Cursor{After: pushAfter}, batchSize)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	if len(evs) == 0 {
		return nil
	}
	pbBatch := &pbmsg.PushBatch{Events: make([]*pbmsg.RepEvent, 0, len(evs))}
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
		pbBatch.Events = append(pbBatch.Events, &pbmsg.RepEvent{
			Time:  timestamppb.New(e.Time),
			Type:  string(e.Type),
			Id:    e.ID,
			Entry: pe,
		})
	}
	body, _ := proto.Marshal(pbBatch)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/replicate/push", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote %s push failed: %s", name, strings.TrimSpace(string(b)))
	}
	var pr pbmsg.PushResult
	if data, err := io.ReadAll(resp.Body); err == nil && len(data) > 0 {
		_ = proto.Unmarshal(data, &pr)
	}
	newPush := nextCur.After
	if newPush.IsZero() && len(evs) > 0 {
		newPush = evs[len(evs)-1].Time
	}
	s.saveCursors(name, newPush, pullAfter)
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

func (s *Service) saveCursors(name string, pushAfter, pullAfter time.Time) {
	p := s.cursorPath(name)
	cf := cursorsFile{}
	if !pushAfter.IsZero() {
		cf.PushAfter = pushAfter.UTC().Format(time.RFC3339Nano)
	}
	if !pullAfter.IsZero() {
		cf.PullAfter = pullAfter.UTC().Format(time.RFC3339Nano)
	}
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

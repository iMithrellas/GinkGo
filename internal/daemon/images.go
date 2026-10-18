package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voiceevents"
)

func (v *voiceRuntime) imageContext(ctx context.Context, m ipc.Message) ipc.Response {
	switch m.Name {
	case "context.stage":
		images, err := imagecontext.Validate(ctx, m.Images)
		if err != nil {
			return ipc.Response{Msg: err.Error()}
		}
		if err := v.pendingImages.Stage(m.Namespace, images); err != nil {
			return ipc.Response{Msg: err.Error()}
		}
		return ipc.Response{OK: true, Msg: fmt.Sprintf("%d image(s) ready for the next AI note in %s (expires in 2 minutes)", len(images), m.Namespace)}
	case "context.clear":
		v.pendingImages.Take(m.Namespace)
		return ipc.Response{OK: true, Msg: "Pending image context cleared"}
	case "context.status":
		return ipc.Response{OK: true, Msg: fmt.Sprintf("%d pending image(s) in %s", v.pendingImages.Count(m.Namespace), m.Namespace)}
	default:
		return ipc.Response{Msg: "unknown image context action"}
	}
}

type imageJob struct {
	cancel context.CancelFunc
	done   chan struct{}
	images []imagecontext.Image
	err    error
}

// sessionImages tracks captures by voice session. The ingest mutex protects jobs;
// each job's done channel makes its result available to the consumer.
type sessionImages struct {
	jobs      map[string]*imageJob
	wg        sync.WaitGroup
	v         *voiceRuntime
	message   ipc.Message
	automatic bool
	ctx       context.Context
}

func newSessionImages(ctx context.Context, v *voiceRuntime, m ipc.Message) *sessionImages {
	s := &sessionImages{
		jobs:    make(map[string]*imageJob),
		v:       v,
		message: m,
		ctx:     ctx,
	}
	if m.Save && m.Mode == "ai" && v.autoScreenshot && v.ai != nil {
		s.automatic = v.ai.ImageSupport(ctx, m.Provider) == ai.Supported
		if !s.automatic && ctx.Err() == nil && v.events != nil {
			_ = v.events.Publish(voiceevents.Event{Type: "error", Error: "Automatic screenshots skipped: model or adapter image support is unsupported or unknown"})
		}
	}
	return s
}

func (s *sessionImages) start(id string) {
	if !s.message.Save || s.message.Mode != "ai" {
		return
	}
	var pending []imagecontext.Image
	if s.v.pendingImages != nil {
		pending = s.v.pendingImages.Take(s.message.Namespace)
	}
	if len(pending) == 0 && !s.automatic {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	j := &imageJob{cancel: cancel, done: make(chan struct{})}
	s.jobs[id] = j
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(j.done)
		defer cancel()
		if len(pending) != 0 {
			j.images = pending
			return
		}
		j.images, j.err = imagecontext.Capture(ctx, s.v.screenshotResolver, s.v.screenshotBackend, s.v.screenshotMode)
	}()
}

func (s *sessionImages) cancel(id string) {
	if j := s.jobs[id]; j != nil {
		j.cancel()
		delete(s.jobs, id)
	}
}

func (s *sessionImages) finish() {
	for _, j := range s.jobs {
		j.cancel()
	}
	s.wg.Wait()
	clear(s.jobs)
}

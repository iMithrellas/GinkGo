package sherpavoice

import (
	"errors"
	"sync"
)

type decodeJob struct {
	id        string
	span      Span
	samples   []float32
	final     bool
	revision  uint64
	selection *selection
}
type decodeResult struct {
	job  decodeJob
	text string
	err  error
}

// decodeQueue preserves finals and coalesces previews to one pending snapshot.
// Overflow is a fault rather than silently losing finalized selected speech.
type decodeQueue struct {
	mu              sync.Mutex
	cond            *sync.Cond
	finals          []decodeJob
	preview         *decodeJob
	closed, discard bool
	limit           int
}

func newQueue(limit int) *decodeQueue {
	q := &decodeQueue{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	return q
}
func (q *decodeQueue) put(j decodeJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return errors.New("decode queue closed")
	}
	if j.final {
		if len(q.finals) >= q.limit {
			return errors.New("decode queue overflow")
		}
		q.finals = append(q.finals, j)
		if q.preview != nil && q.preview.id == j.id {
			q.preview = nil
		}
	} else {
		q.preview = &j
	}
	q.cond.Signal()
	return nil
}
func (q *decodeQueue) get() (decodeJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for !q.closed && len(q.finals) == 0 && q.preview == nil {
		q.cond.Wait()
	}
	if q.discard {
		return decodeJob{}, false
	}
	if len(q.finals) > 0 {
		j := q.finals[0]
		q.finals[0] = decodeJob{}
		q.finals = q.finals[1:]
		return j, true
	}
	if q.preview != nil {
		j := *q.preview
		q.preview = nil
		return j, true
	}
	return decodeJob{}, false
}
func (q *decodeQueue) close(discard bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.discard = discard
	q.preview = nil
	if discard {
		q.finals = nil
	}
	q.cond.Broadcast()
}

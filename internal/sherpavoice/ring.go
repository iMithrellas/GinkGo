package sherpavoice

import "errors"

// sampleRing retains only the bounded recent sample clock, never pads missing
// audio (which could silently shift model-derived boundaries).
type sampleRing struct {
	data []float32
	end  int64
}

func newRing(capacity int) *sampleRing { return &sampleRing{data: make([]float32, capacity)} }
func (r *sampleRing) start() int64     { return max(0, r.end-int64(len(r.data))) }
func (r *sampleRing) append(p []float32) {
	for _, v := range p {
		r.data[r.end%int64(len(r.data))] = v
		r.end++
	}
}
func (r *sampleRing) snapshot(s Span) ([]float32, error) {
	if s.Start < r.start() || s.End > r.end || s.Start >= s.End {
		return nil, errors.New("audio window unavailable")
	}
	p := make([]float32, s.End-s.Start)
	for i := range p {
		p[i] = r.data[(s.Start+int64(i))%int64(len(r.data))]
	}
	return p, nil
}

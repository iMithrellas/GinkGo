package sherpavoice

// submitSelectedPreview shares the resident ASR worker with final recognition.
// The queue coalesces previews, and finals always take priority.
func (e *Engine) submitSelectedPreview(s *selection) error {
	span := Span{max(s.start, e.ring.end-int64(e.config.PreviewSamples)), e.ring.end}
	samples, err := e.ring.snapshot(span)
	if err != nil {
		return err
	}
	if s.previewID == "" {
		s.previewID = e.id("preview")
	}
	s.previewRevision++
	s.previewAt = e.ring.end
	return e.queue.put(decodeJob{id: s.previewID, span: span, samples: samples, revision: s.previewRevision, preview: s})
}

// closeSelectedPreview reuses the selected final rather than decoding the same
// audio a second time for captions. A canceled preview ends at its last result.
func (e *Engine) closeSelectedPreview(s *selection, text string, span Span) error {
	if s.previewClosed {
		return nil
	}
	s.previewClosed = true
	e.queue.discardPreview(s.previewID)
	if !s.previewPublished {
		return nil
	}
	s.previewRevision++
	return e.emit(Event{Type: "caption", ID: s.previewID, Revision: s.previewRevision, Final: true, Span: span, Original: text})
}

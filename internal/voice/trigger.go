package voice

import (
	"context"
	"errors"
	"time"
)

// TriggerDetection identifies the acoustic phrase in the capture's absolute
// sample clock. EndSample is exclusive; following audio may contain dictation
// already, even when detection arrives asynchronously.
type TriggerDetection struct {
	StartSample int64
	EndSample   int64
}

// AcousticTrigger is an injection point for an installed, trained wake detector.
// No implementation/model is bundled. Text configuration is NOT an acoustic
// model. Implementations must preserve offsets and bound their own work/queues.
type AcousticTrigger interface {
	Detect(context.Context, PCMChunk) ([]TriggerDetection, error)
}

// NoteSessionConfig bounds in-memory audio and activation policy. Zero values
// select 5s pre-roll/detection history, 5s trigger-only timeout, 2s cooldown, and
// 2m maximum note duration. Enabled must be explicit, independently of captions.
type NoteSessionConfig struct {
	Enabled        bool
	PreRoll        time.Duration
	TriggerTimeout time.Duration
	Cooldown       time.Duration
	MaxDuration    time.Duration
}

// NoteSessionState exposes note gating without coupling to IPC, STT or storage.
type NoteSessionState struct {
	State         string // disabled, armed, recording, finalizing, canceled
	RecordingNote bool
	SessionID     uint64 // local to this machine; namespace across capture restarts
	StartSample   int64
	DictationFrom int64 // trigger end; do not strip words elsewhere in dictation
	HasDictation  bool
	Reason        string
}

// NoteSessionResult is returned exactly once per finalization/cancellation. WAV
// is nil for canceled sessions, including trigger-only timeout. Nonempty WAV is
// not authorization to save empty/hallucinated text: caller still validates STT.
type NoteSessionResult struct {
	NoteSessionState
	EndSample int64
	WAV       []byte
}

// ErrTriggerExpired means a delayed detection fell outside retained PCM. The
// caller must not start a truncated note or guess an earlier phrase boundary.
var ErrTriggerExpired = errors.New("trigger is outside retained PCM history")

// NoteSession is a single-owner, deterministic note gate. Serialize all methods
// on the capture owner goroutine and pass a monotonic clock's time to commands.
// FeedPCM continuously, even while armed: it only retains bounded RAM history,
// never creates a note without an explicit Trigger or Begin. Captions are wholly
// independent. Construct a fresh machine for each capture stream/sample clock.
type NoteSession struct {
	config                           NoteSessionConfig
	state                            NoteSessionState
	pre, audio                       []byte
	next                             int64
	deadline, maximum, cooldownUntil time.Time
	usedThrough                      int64
	triggered                        bool
}

// NewNoteSession constructs a lazy in-memory gate. Disabled gates ignore invalid
// options and reject commands before inspecting PCM or triggers.
func NewNoteSession(c NoteSessionConfig) (*NoteSession, error) {
	if !c.Enabled {
		return &NoteSession{config: c, state: NoteSessionState{State: "disabled"}}, nil
	}
	if c.PreRoll == 0 {
		c.PreRoll = 5 * time.Second
	}
	if c.TriggerTimeout == 0 {
		c.TriggerTimeout = 5 * time.Second
	}
	if c.Cooldown == 0 {
		c.Cooldown = 2 * time.Second
	}
	if c.MaxDuration == 0 {
		c.MaxDuration = 2 * time.Minute
	}
	if c.PreRoll < captureFrameDuration || c.PreRoll > 30*time.Second ||
		c.TriggerTimeout < captureFrameDuration || c.TriggerTimeout > 30*time.Second ||
		c.Cooldown < 0 || c.Cooldown > time.Minute ||
		c.MaxDuration < captureMinimumSpeech || c.MaxDuration > 5*time.Minute {
		return nil, errors.New("invalid note session bounds (pre-roll 10ms–30s, trigger timeout 10ms–30s, cooldown <=1m, maximum 150ms–5m)")
	}
	return &NoteSession{config: c, state: NoteSessionState{State: "armed"}, pre: make([]byte, 0, durationPCMBytes(c.PreRoll))}, nil
}

func durationPCMBytes(d time.Duration) int { return int(d/captureFrameDuration) * captureFrameBytes }

// State returns a value snapshot, never mutable audio.
func (n *NoteSession) State() NoteSessionState { return n.state }

// FeedPCM accepts contiguous, even-length chunks of at most one second. now and
// Tick enforce wall limits; sample count independently enforces audio limits.
// A chunk spanning the maximum is trimmed at the exact bound, not duplicated.
func (n *NoteSession) FeedPCM(now time.Time, chunk PCMChunk) (*NoteSessionResult, error) {
	if !n.config.Enabled {
		return nil, ErrDisabled
	}
	if chunk.StartSample != n.next || len(chunk.PCM) == 0 || len(chunk.PCM)%2 != 0 || len(chunk.PCM) > 32000 {
		return nil, errors.New("note PCM must be contiguous mono PCM16 chunks of 1–16000 samples")
	}
	result := n.Tick(now)
	n.next += int64(len(chunk.PCM) / 2)
	// Shift in place to keep a hard allocation bound, including long idle input.
	p := chunk.PCM
	if len(p) > cap(n.pre) {
		p = p[len(p)-cap(n.pre):]
	}
	if excess := len(n.pre) + len(p) - cap(n.pre); excess > 0 {
		copy(n.pre, n.pre[excess:])
		n.pre = n.pre[:len(n.pre)-excess]
	}
	n.pre = append(n.pre, p...)
	if n.state.RecordingNote {
		remaining := cap(n.audio) - len(n.audio)
		n.audio = append(n.audio, chunk.PCM[:min(remaining, len(chunk.PCM))]...)
		if len(n.audio) == cap(n.audio) {
			return n.finish(now, "max_duration", false), nil
		}
	}
	return result, nil
}

func (n *NoteSession) begin(now time.Time, start, dictation int64, triggered bool) {
	id := n.state.SessionID + 1
	n.state = NoteSessionState{State: "recording", RecordingNote: true, SessionID: id, StartSample: start, DictationFrom: dictation}
	n.audio = make([]byte, 0, durationPCMBytes(n.config.MaxDuration))
	n.deadline = now.Add(n.config.TriggerTimeout)
	n.maximum = now.Add(n.config.MaxDuration)
	n.triggered = triggered
}

// Trigger activates only while armed and outside cooldown. false,nil means a
// repeated/stale/busy detection was ignored. Audio from the phrase's exact start
// through the current capture position is copied from bounded history; earlier
// background speech is not copied. Expired detection returns an explicit error.
func (n *NoteSession) Trigger(now time.Time, d TriggerDetection) (bool, error) {
	if !n.config.Enabled {
		return false, ErrDisabled
	}
	if d.StartSample < 0 || d.EndSample <= d.StartSample || d.EndSample > n.next {
		return false, errors.New("invalid trigger sample range")
	}
	if n.state.State != "armed" || now.Before(n.cooldownUntil) || d.StartSample < n.usedThrough {
		return false, nil
	}
	oldest := n.next - int64(len(n.pre)/2)
	if d.StartSample < oldest {
		return false, ErrTriggerExpired
	}
	length := int((n.next - d.StartSample) * 2)
	if length >= durationPCMBytes(n.config.MaxDuration) {
		return false, ErrTriggerExpired
	}
	n.begin(now, d.StartSample, d.EndSample, true)
	n.audio = append(n.audio, n.pre[len(n.pre)-length:]...)
	return true, nil
}

// Begin manually starts at the next sample, bypassing acoustic cooldown but not
// an active/finalizing session. ConfirmDictation is still required before End.
func (n *NoteSession) Begin(now time.Time) (bool, error) {
	if !n.config.Enabled {
		return false, ErrDisabled
	}
	if n.state.State != "armed" {
		return false, nil
	}
	n.begin(now, n.next, n.next, false)
	return true, nil
}

// ConfirmDictation marks verified non-trigger content at an absolute sample
// offset. The caller must supply semantic evidence (e.g. a nonempty transcript
// after the leading trigger), not merely the wake phrase or a provisional text
// match that might be revised away. No text matching/removal is done here.
func (n *NoteSession) ConfirmDictation(sample int64) bool {
	if !n.state.RecordingNote || sample < n.state.DictationFrom || sample >= n.next {
		return false
	}
	n.state.HasDictation = true
	return true
}

func (n *NoteSession) finish(now time.Time, reason string, cancel bool) *NoteSessionResult {
	if !n.state.RecordingNote {
		return nil
	}
	n.state.RecordingNote = false
	n.state.Reason = reason
	n.state.State = "finalizing"
	if cancel || !n.state.HasDictation {
		n.state.State = "canceled"
	}
	r := &NoteSessionResult{NoteSessionState: n.state, EndSample: n.state.StartSample + int64(len(n.audio)/2)}
	if n.state.State == "finalizing" {
		r.WAV = captureWAV(n.audio)
	}
	n.audio = nil
	n.usedThrough = n.next
	n.cooldownUntil = now.Add(n.config.Cooldown)
	return r
}

// End finalizes manual/stop/endpoint audio; a trigger-only session is canceled.
func (n *NoteSession) End(now time.Time) *NoteSessionResult { return n.finish(now, "end", false) }

// Cancel discards note audio; it does not stop continuous captions/capture.
func (n *NoteSession) Cancel(now time.Time) *NoteSessionResult { return n.finish(now, "cancel", true) }

// Tick enforces trigger-only and wall-clock maximum timeouts. Call regularly
// even if the microphone stops delivering frames. It never sleeps or does I/O.
func (n *NoteSession) Tick(now time.Time) *NoteSessionResult {
	if !n.state.RecordingNote {
		return nil
	}
	if !now.Before(n.maximum) {
		return n.finish(now, "max_duration", false)
	}
	if n.triggered && !n.state.HasDictation && !now.Before(n.deadline) {
		return n.finish(now, "trigger_timeout", true)
	}
	return nil
}

// Rearm acknowledges a canceled/finalizing result after the caller has handled
// it. Cooldown and the consumed sample watermark survive acknowledgement.
func (n *NoteSession) Rearm() bool {
	if n.state.State != "canceled" && n.state.State != "finalizing" {
		return false
	}
	n.state = NoteSessionState{State: "armed", SessionID: n.state.SessionID}
	return true
}

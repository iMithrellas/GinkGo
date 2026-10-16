package ipc

import (
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

// Message is a minimal command payload sent from CLI to daemon.
type Message struct {
	Name        string          `json:"name"`
	Args        []string        `json:"args,omitempty"`
	ID          string          `json:"id,omitempty"`
	Title       string          `json:"title,omitempty"`
	Body        string          `json:"body,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	IfVersion   int64           `json:"if_version,omitempty"`
	Namespace   string          `json:"namespace,omitempty"`
	Since       string          `json:"since,omitempty"`
	Until       string          `json:"until,omitempty"`
	TagsAny     []string        `json:"tags_any,omitempty"`
	TagsAll     []string        `json:"tags_all,omitempty"`
	Limit       int             `json:"limit,omitempty"`
	Cursor      string          `json:"cursor,omitempty"`
	Reverse     bool            `json:"reverse,omitempty"`
	IncludeBody bool            `json:"include_body,omitempty"`
	Remote      string          `json:"remote,omitempty"`
	SortBy      string          `json:"sort_by,omitempty"`
	Text        string          `json:"text,omitempty"`
	Provider    string          `json:"provider,omitempty"`
	Action      string          `json:"action,omitempty"`
	NoteID      string          `json:"note_id,omitempty"`
	Deadline    time.Time       `json:"deadline,omitempty"`
	Provenance  *api.Provenance `json:"provenance,omitempty"`
	AudioPath   string          `json:"audio_path,omitempty"`
	Save        bool            `json:"save,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Model       string          `json:"model,omitempty"`
	Language    string          `json:"language,omitempty"`
}

// Transcript is the exact local STT output and its engine metadata.
type Transcript struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
	Engine   string `json:"engine"`
	Model    string `json:"model"`
}

// Response is a minimal daemon reply.
type Response struct {
	OK             bool          `json:"ok"`
	Msg            string        `json:"msg,omitempty"`
	Entry          *api.Entry    `json:"entry,omitempty"`
	Entries        []api.Entry   `json:"entries,omitempty"`
	Queue          []QueueRemote `json:"queue,omitempty"`
	Namespaces     []string      `json:"namespaces,omitempty"`
	Tags           []api.TagStat `json:"tags,omitempty"`
	Page           api.Page      `json:"page,omitempty"`
	Action         string        `json:"action,omitempty"`
	FallbackReason string        `json:"fallback_reason,omitempty"`
	Transcript     *Transcript   `json:"transcript,omitempty"`
}

type QueueEvent struct {
	Time time.Time `json:"time"`
	Type string    `json:"type"`
	ID   string    `json:"id"`
}

type QueueRemote struct {
	Name    string       `json:"name"`
	URL     string       `json:"url"`
	Pending int64        `json:"pending"`
	Events  []QueueEvent `json:"events"`
}

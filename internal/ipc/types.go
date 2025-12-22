package ipc

import (
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

// Message is a minimal command payload sent from CLI to daemon.
type Message struct {
	Name      string   `json:"name"`
	Args      []string `json:"args,omitempty"`
	ID        string   `json:"id,omitempty"`
	Title     string   `json:"title,omitempty"`
	Body      string   `json:"body,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	IfVersion int64    `json:"if_version,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Since     string   `json:"since,omitempty"`
	Until     string   `json:"until,omitempty"`
	TagsAny   []string `json:"tags_any,omitempty"`
	TagsAll   []string `json:"tags_all,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Remote    string   `json:"remote,omitempty"`
}

// Response is a minimal daemon reply.
type Response struct {
	OK         bool          `json:"ok"`
	Msg        string        `json:"msg,omitempty"`
	Entry      *api.Entry    `json:"entry,omitempty"`
	Entries    []api.Entry   `json:"entries,omitempty"`
	Queue      []QueueRemote `json:"queue,omitempty"`
	Namespaces []string      `json:"namespaces,omitempty"`
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

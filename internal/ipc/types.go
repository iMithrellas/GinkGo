package ipc

import "github.com/mithrel/ginkgo/pkg/api"

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
}

// Response is a minimal daemon reply.
type Response struct {
	OK      bool        `json:"ok"`
	Msg     string      `json:"msg,omitempty"`
	Entry   *api.Entry  `json:"entry,omitempty"`
	Entries []api.Entry `json:"entries,omitempty"`
}

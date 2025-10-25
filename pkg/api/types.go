package api

import "time"

type Entry struct {
	ID        string    `json:"id"`
	Version   int64     `json:"version"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Namespace string    `json:"namespace"`
}

type EventType string

const (
	EventUpsert EventType = "upsert"
	EventDelete EventType = "delete"
)

type Event struct {
	Time  time.Time `json:"time"`
	Type  EventType `json:"type"`
	Entry *Entry    `json:"entry,omitempty"`
	ID    string    `json:"id"`
}

// Cursor can be extended later for pagination.
type Cursor struct {
	After time.Time `json:"after"`
}

// ListQuery filters listing of entries.
type ListQuery struct {
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit"`
}

// SearchQuery models a query for entry search.
type SearchQuery struct {
	Namespace string `json:"namespace"`
	Query     string `json:"query"`
	Regex     bool   `json:"regex"`
	Limit     int    `json:"limit"`
}

// Page describes pagination cursors for list/search results.
type Page struct {
	Next string `json:"next"`
}

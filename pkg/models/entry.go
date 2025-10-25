package models

import (
	"time"
)

// ULIDs for IDs; store as plain strings here.

// Entry is the materialized note.
type Entry struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Namespace string    `json:"namespace"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags,omitempty"`
	Body      []byte    `json:"body"` // exact Markdown bytes
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int64     `json:"version"` // CAS
}

type EventType string

const (
	EventEntryCreated EventType = "entry.created"
	EventEntryUpdated EventType = "entry.updated" // body/title
	EventEntryDeleted EventType = "entry.deleted"
	EventTagsAdded    EventType = "entry.tags.added"
	EventTagsRemoved  EventType = "entry.tags.removed"
)

// Event is the immutable log record used for replication.
type Event struct {
	ID         string    `json:"id"`       // event ULID
	EntryID    string    `json:"entry_id"` // target entry
	UserID     string    `json:"user_id"`
	Namespace  string    `json:"namespace"`
	Type       EventType `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	Title      *string   `json:"title,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	Body       []byte    `json:"body,omitempty"`
	Version    int64     `json:"version,omitempty"`
}

// Cursor is an opaque pagination token returned by list/search/log APIs.
type Cursor string

// Page describes a single page of results.
type Page struct {
	NextCursor Cursor `json:"next_cursor,omitempty"` // empty => no more pages
	Count      int    `json:"count"`                 // items returned
	Limit      int    `json:"limit"`                 // requested limit
}

// ListQuery filters entries for listing.
type ListQuery struct {
	UserID    string
	Namespace string
	Since     time.Time
	Until     time.Time
	TagsAny   []string // match if entry has ANY of these tags
	TagsAll   []string // match if entry has ALL of these tags
	Limit     int
	Cursor    Cursor
	OrderDesc bool // default true: CreatedAt DESC
}

// SearchQuery supports regex/FTS hybrid search.
type SearchQuery struct {
	UserID    string
	Namespace string
	// Regex is the user-supplied pattern (Go/RE2 semantics).
	Regex string
	// Prefilter is a required literal/term extracted from Regex for FTS/trigram narrowing.
	Prefilter string
	Since     time.Time
	Until     time.Time
	Limit     int
	Cursor    Cursor
}

// NewEntry creates a minimal Entry with timestamps/version set.
func NewEntry(id, userID, ns string, title string, tags []string, body []byte, now time.Time) Entry {
	return Entry{
		ID:        id,
		UserID:    userID,
		Namespace: ns,
		Title:     title,
		Tags:      append([]string(nil), tags...),
		Body:      append([]byte(nil), body...),
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Version:   1,
	}
}

// Touch updates UpdatedAt (call before persisting an update).
func (e *Entry) Touch(now time.Time) { e.UpdatedAt = now.UTC() }

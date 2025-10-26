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
	Namespace string    `json:"namespace"`
	Limit     int       `json:"limit"`
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
}

// SearchQuery models a query for entry search.
type SearchQuery struct {
	Namespace string    `json:"namespace"`
	Query     string    `json:"query"`
	Regex     bool      `json:"regex"`
	Limit     int       `json:"limit"`
	Any       []string  `json:"any"`
	All       []string  `json:"all"`
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
}

// Page describes pagination cursors for list/search results.
type Page struct {
	Next string `json:"next"`
}

// TagStat reports a tag with the number of notes using it
// and an optional human description.
type TagStat struct {
	Tag         string `json:"tag"`
	Count       int    `json:"count"`
	Description string `json:"description,omitempty"`
}

// TagsQuery filters tag listing.
type TagsQuery struct {
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit"`
	Prefix    string `json:"prefix"`
}

// TagFilterQuery specifies tag-based filtering for entries.
// Any: match if note contains at least one of these tags.
// All: match if note contains all of these tags.
type TagFilterQuery struct {
	Namespace string    `json:"namespace"`
	Any       []string  `json:"any"`
	All       []string  `json:"all"`
	Limit     int       `json:"limit"`
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
}

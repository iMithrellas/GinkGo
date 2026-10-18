package api

import "time"

// Provenance retains the ordered original inputs contributing to an entry.
type Provenance struct {
	Sources []Source `json:"sources"`
}

// Source is an application-captured original, never model-generated content.
type Source struct {
	ID           string            `json:"id"`
	CapturedAt   time.Time         `json:"captured_at"`
	Kind         string            `json:"kind"` // "voice" or "text"
	OriginalText string            `json:"original_text"`
	Language     string            `json:"language,omitempty"`
	STTEngine    string            `json:"stt_engine,omitempty"`
	STTModel     string            `json:"stt_model,omitempty"`
	Images       []ImageAttachment `json:"images,omitempty"`
}

// ImageAttachment references immutable original image bytes stored outside the entry.
type ImageAttachment struct {
	ID         string    `json:"id"`
	SHA256     string    `json:"sha256"`
	MediaType  string    `json:"media_type"`
	Size       int64     `json:"size"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	CapturedAt time.Time `json:"captured_at"`
	Label      string    `json:"label,omitempty"`
}

// Images returns the image references in original-input order.
func (e Entry) Images() []ImageAttachment {
	var result []ImageAttachment
	if e.Provenance != nil {
		for _, source := range e.Provenance.Sources {
			result = append(result, source.Images...)
		}
	}
	return result
}

type Entry struct {
	ID         string      `json:"id"`
	Version    int64       `json:"version"`
	Title      string      `json:"title"`
	Body       string      `json:"body"`
	Tags       []string    `json:"tags"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	Namespace  string      `json:"namespace"`
	Provenance *Provenance `json:"provenance,omitempty"`
}

type EventType string

const (
	EventUpsert EventType = "upsert"
	EventDelete EventType = "delete"
)

type Event struct {
	Time        time.Time `json:"time"`
	Type        EventType `json:"type"`
	Entry       *Entry    `json:"entry,omitempty"`
	ID          string    `json:"id"`
	Namespace   string    `json:"namespace,omitempty"`
	PayloadType string    `json:"payload_type,omitempty"`
	Payload     []byte    `json:"payload,omitempty"`
	OriginLabel string    `json:"origin_label,omitempty"`
	SignerID    string    `json:"signer_id,omitempty"`
	Sig         []byte    `json:"sig,omitempty"`
}

// Cursor can be extended later for pagination.
type Cursor struct {
	After time.Time `json:"after"`
}

// SearchQuery models a query for entry search.
type SearchQuery struct {
	Namespace   string    `json:"namespace"`
	Query       string    `json:"query"`
	Regex       bool      `json:"regex"`
	Limit       int       `json:"limit"`
	Any         []string  `json:"any"`
	All         []string  `json:"all"`
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	Cursor      string    `json:"cursor,omitempty"`
	Reverse     bool      `json:"reverse,omitempty"`
	IncludeBody bool      `json:"include_body,omitempty"`
}

// Page describes pagination cursors for list/search results.
type Page struct {
	Next string `json:"next"`
	Prev string `json:"prev"`
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
	SortBy    string `json:"sort_by"` // "count", "frecency"
}

// ListQuery specifies tag-based filtering for entries.
// Any: match if note contains at least one of these tags.
// All: match if note contains all of these tags.
type ListQuery struct {
	Namespace   string    `json:"namespace"`
	Any         []string  `json:"any,omitempty"`
	All         []string  `json:"all,omitempty"`
	Limit       int       `json:"limit"`
	Since       time.Time `json:"since,omitempty"`
	Until       time.Time `json:"until,omitempty"`
	Cursor      string    `json:"cursor,omitempty"`
	Reverse     bool      `json:"reverse,omitempty"`
	IncludeBody bool      `json:"include_body,omitempty"`
}

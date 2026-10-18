package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/mithrel/ginkgo/pkg/api"
)

func encodeProvenance(p *api.Provenance) (any, error) {
	if p == nil {
		return nil, nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode provenance: %w", err)
	}
	return string(data), nil
}

func decodeProvenance(data sql.NullString, e *api.Entry) error {
	if !data.Valid {
		return nil
	}
	if err := json.Unmarshal([]byte(data.String), &e.Provenance); err != nil {
		return fmt.Errorf("decode provenance for entry %s: %w", e.ID, err)
	}
	return nil
}

// preserveProvenance makes sources append-only. Omission by older IPC clients or
// incoming replication writers preserves the locally stored originals. Explicit
// replacement that drops, reorders, or changes originals is rejected atomically.
func preserveProvenance(e *api.Entry, current *api.Provenance) error {
	if e.Provenance == nil {
		e.Provenance = current
		return nil
	}
	if current == nil {
		return nil
	}
	if !sourcePrefix(current, e.Provenance) {
		return fmt.Errorf("provenance sources are append-only: %w", ErrConflict)
	}
	return nil
}

// sourcePrefix compares original records by value, with capture times compared
// by instant rather than their serialized timezone. Absent provenance is an
// empty history, including snapshots predating the first captured source.
func sourcePrefix(prefix, history *api.Provenance) bool {
	if prefix == nil {
		return true
	}
	if history == nil {
		return len(prefix.Sources) == 0
	}
	if len(prefix.Sources) > len(history.Sources) {
		return false
	}
	for i, old := range prefix.Sources {
		next := history.Sources[i]
		if old.ID != next.ID || !old.CapturedAt.Equal(next.CapturedAt) ||
			old.Kind != next.Kind || old.OriginalText != next.OriginalText ||
			old.Language != next.Language || old.STTEngine != next.STTEngine ||
			old.STTModel != next.STTModel || !slices.EqualFunc(old.Images, next.Images, sameImageReference) {
			return false
		}
	}
	return true
}

func sameImageReference(a, b api.ImageAttachment) bool {
	if !a.CapturedAt.Equal(b.CapturedAt) {
		return false
	}
	a.CapturedAt = b.CapturedAt
	return a == b
}

// replicationSnapshotDone distinguishes replayed history from a conflicting
// writer. Only strictly older snapshots with a compatible source prefix can be
// ignored; never roll back current content/version or discard divergent sources.
func replicationSnapshotDone(current, incoming api.Entry) (bool, error) {
	if incoming.Version < current.Version {
		if !sourcePrefix(incoming.Provenance, current.Provenance) {
			return false, fmt.Errorf("older replication snapshot has divergent provenance: %w", ErrConflict)
		}
		return true, nil
	}
	return incoming.Version == current.Version && current.Hash() == incoming.Hash(), nil
}

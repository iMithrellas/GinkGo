package api

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// NewID generates a simple, sortable-ish ID using time and randomness.
// Not a strict ULID; suitable for initial wireframe use.
func NewID() string {
	now := time.Now().UnixNano()
	ts := strconv.FormatInt(now, 36)
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return ts + "-" + hex.EncodeToString(buf[:])
}

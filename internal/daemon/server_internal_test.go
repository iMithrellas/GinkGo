package daemon

import (
	"testing"
	"time"
)

func TestNormalizeTags(t *testing.T) {
	in := []string{" A ", "a", "B", "", "b ", "C", "a"}
	got := normalizeTags(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestParseBounds(t *testing.T) {
	s, u := parseBounds("2023-01-02T03:04:05Z", "2023-01-03T03:04:05Z")
	if s.IsZero() || u.IsZero() {
		t.Fatalf("expected non-zero times: %v %v", s, u)
	}
	// Expect UTC
	if s.Location() != time.UTC || u.Location() != time.UTC {
		t.Fatalf("expected UTC location")
	}
	// Invalid inputs -> zero values
	zs, zu := parseBounds("not-a-time", "")
	if !zs.IsZero() || !zu.IsZero() {
		t.Fatalf("expected zero values for invalid inputs, got %v %v", zs, zu)
	}
}

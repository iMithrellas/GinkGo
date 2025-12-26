package util

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseTimeExpr parses relative ("2h", "3d", "2w", "1mo") and absolute
// (RFC3339, "2006-01-02T15:04", "2006-01-02") time expressions.
func parseTimeExpr(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time expression")
	}

	// Custom shorthands: mo (months), w (weeks), d (days)
	suffixes := []struct {
		suffix string
		apply  func(int) time.Time
	}{
		{"mo", func(n int) time.Time { return now.AddDate(0, -n, 0) }},
		{"w", func(n int) time.Time { return now.Add(-time.Duration(n*7) * 24 * time.Hour) }},
		{"d", func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }},
	}
	for _, sfx := range suffixes {
		if strings.HasSuffix(s, sfx.suffix) {
			numStr := strings.TrimSuffix(s, sfx.suffix)
			if n, err := strconv.Atoi(numStr); err == nil && n >= 0 {
				return sfx.apply(n), nil
			}
			return time.Time{}, fmt.Errorf("invalid %s duration: %q", sfx.suffix, s)
		}
	}

	// Standard Go durations (keeps 'm' = minutes)
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}

	// Absolutes
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("invalid time expression: %q", s)
}

// NormalizeTimeRange parses since/until (empty allowed) and swaps if reversed.
func NormalizeTimeRange(since, until string) (string, string, error) {
	now := time.Now()

	var s, u time.Time
	var err error

	if since != "" {
		if s, err = parseTimeExpr(since, now); err != nil {
			return "", "", fmt.Errorf("invalid --since: %w", err)
		}
	}
	if until != "" {
		if u, err = parseTimeExpr(until, now); err != nil {
			return "", "", fmt.Errorf("invalid --until: %w", err)
		}
	}

	if !s.IsZero() && !u.IsZero() && s.After(u) {
		s, u = u, s
	}

	var sStr, uStr string
	if !s.IsZero() {
		sStr = s.UTC().Format(time.RFC3339)
	}
	if !u.IsZero() {
		uStr = u.UTC().Format(time.RFC3339)
	}
	return sStr, uStr, nil
}

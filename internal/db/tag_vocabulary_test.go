package db

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

func TestTagFrecency(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		count, days int
		want        float64
	}{
		{1, 0, 2}, {1, 30, 1.5}, {3, 30, 2.5}, {1, -30, 2}, {7, 90, 3.25},
	} {
		if got := tagFrecency(tt.count, now.AddDate(0, 0, -tt.days), now); math.Abs(got-tt.want) > 1e-9 {
			t.Fatalf("%+v: %v", tt, got)
		}
	}
}

func TestRankedTagsScopeFrequencyRecencyRelevanceAndLimits(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "tags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seed := func(id, ns string, days int, tags ...string) {
		t.Helper()
		date := now.AddDate(0, 0, -days)
		_, err := store.Entries.CreateEntry(ctx, api.Entry{ID: id, Namespace: ns, Title: id, Tags: tags, Version: 1, CreatedAt: date, UpdatedAt: date})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("a", "work", 0, "alpha", "fresh", "frequent")
	seed("b", "work", 30, "frequent")
	seed("c", "work", 30, "stale", "deployment")
	seed("d", "private", 0, "private-only", "private-only-2")
	// Legacy case/whitespace variants count once per note after normalization.
	seed("legacy", "legacy", 0, " Mixed ", "mixed", "MIXED", " ")
	seed("single", "legacy", 0, "alpha")
	vocabulary := store.Entries.(TagVocabulary)
	legacy, err := vocabulary.RankedTags(ctx, "legacy", "", now, 40)
	if err != nil || !reflect.DeepEqual(legacy, []string{"alpha", "mixed"}) {
		t.Fatalf("normalized frequency was inflated: %v %v", legacy, err)
	}
	got, err := vocabulary.RankedTags(ctx, "work", "Deployment plan", now, 40)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deployment", "frequent", "alpha", "fresh", "stale"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got, err = vocabulary.RankedTags(ctx, "work", "unrelated", now, 2)
	if err != nil || !reflect.DeepEqual(got, []string{"frequent", "alpha"}) {
		t.Fatalf("%v %v", got, err)
	}
	// Relevant tags must survive the top-40 cutoff even when rare and old.
	for i := 0; i < 60; i++ {
		seed(fmt.Sprint("more", i), "work", 0, fmt.Sprintf("tag%02d", i))
	}
	for _, limit := range []int{0, 40, 1000} {
		got, err = vocabulary.RankedTags(ctx, "work", "deployment", now, limit)
		if err != nil || len(got) != 40 || got[0] != "deployment" {
			t.Fatalf("%v %v", got, err)
		}
	}
	got, err = vocabulary.RankedTags(ctx, "", "private-only", now, 40)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty namespace broadened: %v %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := vocabulary.RankedTags(cancelled, "work", "", now, 40); err == nil {
		t.Fatal("cancellation ignored")
	}
}

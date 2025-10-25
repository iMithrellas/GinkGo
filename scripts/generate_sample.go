package main

import (
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"os"
	"time"
)

type Entry struct {
	ID        string    `json:"id,omitempty"`
	Version   int64     `json:"version"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Namespace string    `json:"namespace"`
}

func main() {
	// Deterministic seed for reproducible output
	mr := mrand.New(mrand.NewSource(42))

	tags := make([]string, 20)
	for i := 0; i < 20; i++ {
		tags[i] = fmt.Sprintf("tag%02d", i+1)
	}

	const total = 500
	out := make([]Entry, 0, total)
	base := time.Now().UTC()

	for i := 0; i < total; i++ {
		ns := "ns1"
		if i%5 == 0 { // ~20%
			ns = "ns2"
		}
		// 1–4 unique tags
		k := 1 + mr.Intn(4)
		chosen := sampleTags(mr, tags, k)

		// Stagger timestamps backwards to look natural
		created := base.Add(-time.Duration(30*i+mr.Intn(60)) * time.Minute)
		// Some entries get later updates; most keep same
		updated := created.Add(time.Duration(mr.Intn(180)) * time.Minute)
		if mr.Float64() < 0.7 {
			updated = created
		}

		n := Entry{
			Version:   1,
			Title:     fmt.Sprintf("Sample Note %03d", i+1),
			Body:      fmt.Sprintf("This is the body for sample note %03d.\n\nTags: %v\nNamespace: %s\n", i+1, chosen, ns),
			Tags:      chosen,
			CreatedAt: created,
			UpdatedAt: updated,
			Namespace: ns,
		}
		out = append(out, n)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err)
	}
}

func sampleTags(r *mrand.Rand, pool []string, k int) []string {
	if k >= len(pool) {
		k = len(pool)
	}
	idx := r.Perm(len(pool))[:k]
	out := make([]string, k)
	for i, j := range idx {
		out[i] = pool[j]
	}
	return out
}

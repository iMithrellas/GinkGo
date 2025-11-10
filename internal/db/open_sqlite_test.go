package db

import "testing"

func TestUniqueStrings(t *testing.T) {
	in := []string{" A ", "a", "B", "b", "", "C", "b", "c"}
	got := uniqueStrings(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestLongestWord(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"hi", ""},
		{"a bc de", ""},
		{"foo bar-baz", "foo"},
		{"Hello, world!!! 1234 12", "hello"},
		{"über-cool naïve café123", "café123"},
		{"abc😊def ghi", "abc"},
		{"go123go4567", "go123go4567"},
	}
	for i, tc := range tests {
		if got := longestWord(tc.in); got != tc.want {
			t.Fatalf("case %d: got %q want %q", i, got, tc.want)
		}
	}
}

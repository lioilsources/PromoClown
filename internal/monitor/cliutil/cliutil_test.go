package cliutil

import (
	"bytes"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"48h":                  now.Add(-48 * time.Hour),
		"7d":                   now.Add(-7 * 24 * time.Hour),
		"90m":                  now.Add(-90 * time.Minute),
		"2026-09-01T00:00:00Z": time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		"":                     now.Add(-48 * time.Hour),
	}
	for in, want := range cases {
		got, err := ParseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseSince("last week", now); err == nil {
		t.Error("nonsense should fail")
	}
}

func TestWriteJSONNeverNull(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON[int](&buf, nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "[]\n" {
		t.Errorf("empty output = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("žluťoučký", 4); got != "žluť…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("Truncate = %q", got)
	}
}

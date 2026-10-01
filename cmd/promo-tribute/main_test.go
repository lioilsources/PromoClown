package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lioilsources/promoclown/internal/model"
)

func TestPostText(t *testing.T) {
	tribute := model.Tribute{Credit: "@artist"}
	project := model.Project{Slug: "tsumiki", WebsiteURL: "https://t.me/tsumikimanga_bot"}
	labels := []string{"Lempicka (Art Deco)", "Chagall"}

	got := postText(defaultTemplate, tribute, project, labels, "TsumikiBot")
	for _, want := range []string{"Tribute to @artist", "Lempicka (Art Deco) · Chagall", "by TsumikiBot"} {
		if !strings.Contains(got, want) {
			t.Errorf("postText = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "{") {
		t.Errorf("postText = %q, an unfilled placeholder remains", got)
	}

	// A credit long enough to push the styled text past 280 still has to keep
	// the credit and the signature — those are the two things the post exists
	// for — and drop the style list instead.
	longLabels := make([]string, 20)
	for i := range longLabels {
		longLabels[i] = "A Very Long Painter Style Name Indeed"
	}
	got = postText(defaultTemplate, tribute, project, longLabels, "TsumikiBot")
	if !strings.Contains(got, "Tribute to @artist") || !strings.Contains(got, "by TsumikiBot") {
		t.Errorf("postText (long) = %q, credit and signature must survive", got)
	}
	if strings.Contains(got, "A Very Long Painter") {
		t.Errorf("postText (long) = %q, the style list should have been dropped", got)
	}

	// {by} defaults to whatever the caller passes, and is absent if asked to be.
	got = postText("{credit} — {by}", tribute, project, nil, "")
	if got != "@artist —" {
		t.Errorf("postText with empty by = %q, want %q", got, "@artist —")
	}
}

// fakeAPI is a promo-api stand-in that records every request it gets.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string // "METHOD /path?query"
	queued   []model.Tribute
	claims   []model.Tribute // handed out one per claim, then 404
	finished map[string]model.TributeDoneRequest
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		req += "?" + r.URL.RawQuery
	}
	f.requests = append(f.requests, req)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/tributes":
		_ = json.NewEncoder(w).Encode(f.queued)
	case r.Method == http.MethodPost && r.URL.Path == "/tributes/claim":
		if len(f.claims) == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"queue is empty"}`))
			return
		}
		t := f.claims[0]
		f.claims = f.claims[1:]
		_ = json.NewEncoder(w).Encode(t)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/done"):
		var body model.TributeDoneRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.finished == nil {
			f.finished = map[string]model.TributeDoneRequest{}
		}
		f.finished[r.URL.Path] = body
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"not in this test"}`))
	}
}

func (f *fakeAPI) mutating() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}

func startFake(t *testing.T, f *fakeAPI) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("PROMO_API_URL", srv.URL)
	t.Setenv("PROMO_TOKEN", "agent-token")
}

// A dry run must leave the queue alone: on 2026-09-30 it claimed tributes
// #34-#36 and gave them back as failures, which only the admin can undo.
func TestDryRunDoesNotTouchQueue(t *testing.T) {
	f := &fakeAPI{
		// newest first, as promo-api lists them
		queued: []model.Tribute{
			{ID: 36, Credit: "@carol", Status: "queued"},
			{ID: 35, Credit: "@bob", Status: "queued"},
			{ID: 34, Credit: "@alice", Status: "queued"},
		},
		claims: []model.Tribute{{ID: 34, Credit: "@alice"}},
	}
	startFake(t, f)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"run", "--dry-run", "--seed", "7"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr.String())
	}
	if m := f.mutating(); len(m) != 0 {
		t.Errorf("dry run sent mutating requests: %v", m)
	}
	if len(f.requests) != 1 || !strings.HasPrefix(f.requests[0], "GET /tributes?") ||
		!strings.Contains(f.requests[0], "status=queued") {
		t.Errorf("requests = %v, want a single GET /tributes?status=queued", f.requests)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout has %d lines, want 3:\n%s", len(lines), stdout.String())
	}
	// Oldest first, the order a real run would claim them in.
	for i, want := range []string{"#34 @alice → ", "#35 @bob → ", "#36 @carol → "} {
		if !strings.HasPrefix(lines[i], want) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], want)
		}
		if ids := strings.Split(strings.TrimPrefix(lines[i], want), ", "); len(ids) != 4 {
			t.Errorf("line %d = %q, want 4 styles", i, lines[i])
		}
	}
	if !strings.Contains(stderr.String(), "queue untouched") {
		t.Errorf("stderr = %q, want it to say the queue was not touched", stderr.String())
	}
}

func TestDryRunRespectsMax(t *testing.T) {
	f := &fakeAPI{queued: []model.Tribute{
		{ID: 36, Credit: "@carol"}, {ID: 35, Credit: "@bob"}, {ID: 34, Credit: "@alice"},
	}}
	startFake(t, f)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"run", "--dry-run", "--max", "2", "--variants", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#34 ") || !strings.HasPrefix(lines[1], "#35 ") {
		t.Errorf("stdout = %q, want #34 and #35 only", stdout.String())
	}
	if m := f.mutating(); len(m) != 0 {
		t.Errorf("dry run sent mutating requests: %v", m)
	}
}

// Without --dry-run the worker still claims, and a tribute that fails is
// still given back with its error so the queue does not keep it "running".
func TestRunClaimsAndRecordsFailure(t *testing.T) {
	f := &fakeAPI{claims: []model.Tribute{{ID: 40, Credit: "@dave", SourcePath: "tributes/x.png"}}}
	startFake(t, f)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"run", "--comfy", "http://127.0.0.1:1"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (the asset download fails), stderr:\n%s", code, stderr.String())
	}
	want := []string{"POST /tributes/claim", "POST /tributes/40/done", "POST /tributes/claim"}
	if got := f.mutating(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("mutating requests = %v, want %v", got, want)
	}
	for _, r := range f.requests {
		if strings.HasPrefix(r, "GET /tributes") {
			t.Errorf("a real run should not list the queue, got %q", r)
		}
	}
	done, ok := f.finished["/tributes/40/done"]
	if !ok || done.Error == "" {
		t.Errorf("tribute #40 finished with %+v, want an error recorded", done)
	}
	if !strings.Contains(stderr.String(), "tributes drafted: 0, failed: 1") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

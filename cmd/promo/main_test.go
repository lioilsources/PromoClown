package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lioilsources/promoclown/internal/api"
	"github.com/lioilsources/promoclown/internal/client"
	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
)

const (
	agentToken = "agent-token-0123456789abcdef-0123"
	adminToken = "admin-token-0123456789abcdef-0123"
)

func startAPI(t *testing.T) string {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	svc := core.New(conn, core.Config{
		AssetsDir: t.TempDir(),
		Schedule:  core.Schedule{Loc: time.UTC, WindowEnd: 24 * time.Hour, Lead: time.Minute},
	})
	srv := httptest.NewServer(api.New(svc, api.Config{AgentToken: agentToken, AdminToken: adminToken}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	_, err = client.New(srv.URL, adminToken).UpsertProjects(context.Background(), []model.Project{{
		Slug: "kiran", Name: "Kiran", Tagline: "Offline trail maps", Hooks: []string{"works without signal"},
		ForbiddenClaims: []string{"free forever"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return srv.URL
}

type result struct {
	code        int
	out, stderr string
}

func promo(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return result{code, out.String(), errb.String()}
}

func TestAgentWorkflow(t *testing.T) {
	t.Setenv("PROMO_API_URL", startAPI(t))
	t.Setenv("PROMO_TOKEN", agentToken)

	r := promo(t, "", "posts", "draft", "--project", "kiran", "--platform", "bluesky", "--text", "Kiran maps work without signal")
	if r.code != 0 || !strings.Contains(r.out, "Draft #1 saved") || !strings.Contains(r.out, "warning: no visual") {
		t.Fatalf("draft: %+v", r)
	}

	r = promo(t, "", "posts", "draft", "--project", "kiran", "--platform", "bluesky", "--text", "Kiran maps work without signal!")
	if r.code != 2 || !strings.Contains(r.stderr, "repeats #1") {
		t.Fatalf("duplicate draft: %+v", r)
	}

	for _, args := range [][]string{{"posts", "approve", "1"}, {"posts", "reject", "1"}, {"projects", "import", "/dev/null"}} {
		r = promo(t, "", args...)
		if r.code == 0 {
			t.Errorf("%v succeeded with the agent token: %+v", args, r)
		}
	}
	r = promo(t, "", "posts", "approve", "1", "--at", "2030-01-01T10:00")
	if r.code != 2 || !strings.Contains(r.stderr, "not allowed") {
		t.Errorf("approve: %+v", r)
	}
	if r = promo(t, "", "posts", "publish", "1"); r.code == 0 || !strings.Contains(r.stderr, "no publish command") {
		t.Errorf("publish: %+v", r)
	}

	r = promo(t, "", "posts", "show", "1", "--json")
	var post model.Post
	if r.code != 0 || json.Unmarshal([]byte(r.out), &post) != nil || post.Status != "draft" {
		t.Fatalf("show --json: %+v", r)
	}

	if r = promo(t, "", "digest", "--silent-if-empty"); r.code != 0 || r.out != "" {
		t.Fatalf("empty digest should be silent: %+v", r)
	}
	r = promo(t, `[{"platform":"bluesky","kind":"mention","external_id":"at://m1","author":"hiker.bsky.social","text":"Kiran saved my trip"}]`,
		"mentions", "import")
	if r.code != 0 || !strings.Contains(r.out, "1 new") {
		t.Fatalf("import: %+v", r)
	}
	if r = promo(t, "", "mentions", "new"); !strings.Contains(r.out, "[kiran]") {
		t.Fatalf("mentions new: %+v", r)
	}
	if r = promo(t, "", "mentions", "new"); !strings.Contains(r.out, "No mentions.") {
		t.Fatalf("second mentions new: %+v", r)
	}
	if r = promo(t, "", "digest", "--silent-if-empty"); !strings.Contains(r.out, "Mentions (1)") {
		t.Fatalf("digest: %+v", r)
	}

	r = promo(t, "", "projects", "list", "--md")
	if !strings.Contains(r.out, "# PROJECTS.md") || !strings.Contains(r.out, "Never claim or promise:\n- free forever") {
		t.Fatalf("projects --md: %s", r.out)
	}
}

func TestSeedProjectsImport(t *testing.T) {
	t.Setenv("PROMO_API_URL", startAPI(t))
	t.Setenv("PROMO_TOKEN", adminToken)
	r := promo(t, "", "projects", "import", "../../projects.yaml")
	if r.code != 0 || !strings.Contains(r.out, "saved kirian (active)") || !strings.Contains(r.out, "saved lexify (paused)") {
		t.Fatalf("import projects.yaml: %+v", r)
	}
	r = promo(t, "", "projects", "list", "--status", "active", "--json")
	var active []model.Project
	if err := json.Unmarshal([]byte(r.out), &active); err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, p := range active {
		slugs = append(slugs, p.Slug)
	}
	// "kiran" comes from startAPI; the seed adds exactly two active projects.
	if got := strings.Join(slugs, ","); got != "doggiowars,kiran,kirian" {
		t.Fatalf("active projects = %s", got)
	}
}

func TestParseFlagsInterspersed(t *testing.T) {
	r := promo(t, "", "posts", "show")
	if r.code == 0 {
		t.Fatal("missing token should fail")
	}
	t.Setenv("PROMO_TOKEN", "x")
	if r = promo(t, "", "posts", "list", "--bogus"); r.code != 1 || !strings.Contains(r.stderr, "unknown flag") {
		t.Fatalf("unknown flag: %+v", r)
	}
}

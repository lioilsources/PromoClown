package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
	agentToken = "agent-token-0123456789abcdef"
	adminToken = "admin-token-0123456789abcdef"
	secret     = "hook-secret"
)

func setup(t *testing.T) (*httptest.Server, chan []byte) {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	loc, _ := time.LoadLocation("Europe/Prague")
	svc := core.New(conn, core.Config{
		AssetsDir: t.TempDir(),
		Schedule:  core.Schedule{Loc: loc, WindowStart: 0, WindowEnd: 24 * time.Hour, Lead: time.Minute},
	})
	hooks := make(chan []byte, 1)
	h := api.New(svc, api.Config{AgentToken: agentToken, AdminToken: adminToken, WebhookSecret: secret},
		func(_ context.Context, body []byte) { hooks <- body },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	admin := client.New(srv.URL, adminToken)
	if _, err := admin.UpsertProjects(context.Background(), []model.Project{{Slug: "kiran", Name: "Kiran"}}); err != nil {
		t.Fatal(err)
	}
	return srv, hooks
}

func status(err error) int {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	if err == nil {
		return 200
	}
	return -1
}

func TestAgentCannotApprove(t *testing.T) {
	srv, _ := setup(t)
	ctx := context.Background()
	agent := client.New(srv.URL, agentToken)
	admin := client.New(srv.URL, adminToken)
	admin.Actor = "cli"

	res, err := agent.Draft(ctx, model.DraftRequest{Project: "kiran", Platform: "bluesky", Text: "Kiran 1.2 is out"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Post.ID
	if res.Post.CreatedBy != core.ActorAgent {
		t.Errorf("created_by = %q", res.Post.CreatedBy)
	}

	text := "sneaky"
	checks := []struct {
		name string
		err  error
	}{
		{"approve", func() error { _, err := agent.Approve(ctx, id, model.ApproveRequest{Revision: 1}); return err }()},
		{"edit", func() error { _, err := agent.Edit(ctx, id, model.EditRequest{Text: &text}); return err }()},
		{"reject", func() error { _, err := agent.Reject(ctx, id, model.RejectRequest{}); return err }()},
		{"published", func() error { _, err := agent.MarkPublished(ctx, id, model.PublishedRequest{}); return err }()},
		{"projects", func() error { _, err := agent.UpsertProjects(ctx, []model.Project{{Slug: "x", Name: "X"}}); return err }()},
	}
	for _, c := range checks {
		if got := status(c.err); got != http.StatusForbidden {
			t.Errorf("agent %s: status %d, want 403 (%v)", c.name, got, c.err)
		}
	}

	if _, err := client.New(srv.URL, "wrong").Posts(ctx, client.PostQuery{}); status(err) != http.StatusUnauthorized {
		t.Errorf("wrong token: %v", err)
	}

	approved, err := admin.Approve(ctx, id, model.ApproveRequest{Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Post.Status != model.StatusApproved || approved.Post.ApprovedBy != "cli" {
		t.Errorf("approved = %+v", approved.Post)
	}
}

func TestAgentCannotSpoofActor(t *testing.T) {
	srv, _ := setup(t)
	agent := client.New(srv.URL, agentToken)
	agent.Actor = "telegram:1"
	res, err := agent.Draft(context.Background(), model.DraftRequest{Project: "kiran", Platform: "x", Text: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Post.CreatedBy != core.ActorAgent {
		t.Errorf("agent spoofed actor: %q", res.Post.CreatedBy)
	}
}

func TestValidationAndUnknownFields(t *testing.T) {
	srv, _ := setup(t)
	agent := client.New(srv.URL, agentToken)
	_, err := agent.Draft(context.Background(), model.DraftRequest{Project: "kiran", Platform: "x", Text: strings.Repeat("a", 300)})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity || len(apiErr.Problems) == 0 {
		t.Fatalf("too long for X: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/posts", strings.NewReader(`{"project":"kiran","platform":"x","content":"typo"}`))
	req.Header.Set(api.TokenHeader, agentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: status %d, want 400", resp.StatusCode)
	}
}

func TestWebhookSecret(t *testing.T) {
	srv, hooks := setup(t)
	post := func(q string) int {
		resp, err := http.Post(srv.URL+"/webhooks/postiz"+q, "application/json", strings.NewReader(`[{"id":"p1"}]`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("?secret=nope"); got != http.StatusUnauthorized {
		t.Errorf("bad secret: %d", got)
	}
	if got := post("?secret=" + secret); got != http.StatusAccepted {
		t.Errorf("good secret: %d", got)
	}
	select {
	case body := <-hooks:
		if !strings.Contains(string(body), "p1") {
			t.Errorf("hook body = %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Error("webhook handler not called")
	}
}

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestService(t *testing.T) (*Service, *clock) {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	assets := t.TempDir()
	if err := os.MkdirAll(filepath.Join(assets, "kiran"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "kiran", "shot.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	loc, _ := time.LoadLocation("Europe/Prague")
	c := &clock{t: time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)} // 10:00 in Prague
	svc := New(conn, Config{
		AssetsDir: assets,
		Schedule:  Schedule{Loc: loc, WindowStart: 9 * time.Hour, WindowEnd: 20 * time.Hour, Lead: 10 * time.Minute},
		Now:       c.now,
	})
	_, err = svc.UpsertProject(context.Background(), model.Project{
		Slug: "kiran", Name: "Kiran", StoreIOSURL: "https://apps.apple.com/app/id1",
		IOSAppID: "1", ForbiddenClaims: []string{"no ads ever"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, c
}

func TestPostLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, c := newTestService(t)

	draft := model.DraftRequest{
		Project: "kiran", Platform: "bluesky",
		Text:       "Kiran 1.2: offline maps for hikers. https://apps.apple.com/app/id1",
		MediaPaths: []string{"kiran/shot.png"},
	}
	res, err := svc.CreateDraft(ctx, draft, ActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	post := res.Post
	if post.Status != model.StatusDraft || post.Revision != 1 || post.Kind != "post" {
		t.Fatalf("new draft = %+v", post)
	}

	// The agent may not repeat itself on the same platform.
	_, err = svc.CreateDraft(ctx, draft, ActorAgent)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.Error(), "repeats") {
		t.Fatalf("duplicate draft: %v", err)
	}

	// Missing media and forbidden claims are refused together.
	_, err = svc.CreateDraft(ctx, model.DraftRequest{
		Project: "kiran", Platform: "x", Text: "No ads ever!", MediaPaths: []string{"kiran/missing.png"},
	}, ActorAgent)
	if !errors.As(err, &verr) || len(verr.Problems) != 2 {
		t.Fatalf("want two problems, got %v", err)
	}

	// The preview went out at revision 1; a human edit makes it revision 2.
	text := "Kiran 1.2 brings offline maps. https://apps.apple.com/app/id1"
	edited, err := svc.EditDraft(ctx, post.ID, model.EditRequest{Text: &text, Actor: "telegram:42"})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Post.Revision != 2 {
		t.Fatalf("revision after edit = %d", edited.Post.Revision)
	}

	_, err = svc.Approve(ctx, post.ID, model.ApproveRequest{Revision: 1, Actor: "telegram:42"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("approving a stale revision: %v", err)
	}

	approved, err := svc.Approve(ctx, post.ID, model.ApproveRequest{Revision: 2, Actor: "telegram:42"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Post.Status != model.StatusApproved || approved.Post.ScheduledAt != "2026-09-14T08:10:00Z" {
		t.Fatalf("approved = %+v", approved.Post)
	}

	// A second Bluesky post the same day lands on the next free day.
	res2, err := svc.CreateDraft(ctx, model.DraftRequest{
		Project: "kiran", Platform: "bluesky", Text: "How we built tile caching in Flutter https://apps.apple.com/app/id1",
		MediaPaths: []string{"kiran/shot.png"},
	}, ActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	approved2, err := svc.Approve(ctx, res2.Post.ID, model.ApproveRequest{Revision: 1, Actor: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if approved2.Post.ScheduledAt != "2026-09-21T08:10:00Z" {
		t.Fatalf("same project within a week should wait seven days, got %s", approved2.Post.ScheduledAt)
	}

	// Publisher and Postiz report progress; only those events need a Telegram note.
	at, _ := db.ParseTime(approved.Post.ScheduledAt)
	if err := svc.MarkScheduled(ctx, post.ID, "pz1", "", at); err != nil {
		t.Fatal(err)
	}
	c.t = at.Add(time.Minute)
	if _, err := svc.MarkPublished(ctx, post.ID, "https://bsky.app/profile/x/post/1", time.Time{}, ActorPostiz); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MarkPublished(ctx, post.ID, "https://bsky.app/profile/x/post/1", time.Time{}, ActorPostiz); err != nil {
		t.Fatalf("second publish report should be a no-op: %v", err)
	}
	events, err := svc.UnnotifiedEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	if strings.Join(actions, ",") != "scheduled,published" {
		t.Fatalf("unnotified events = %v", actions)
	}

	log, err := svc.PostLog(ctx, c.t.Add(-30*24*time.Hour))
	if err != nil || len(log) != 2 {
		t.Fatalf("post log = %v, %v", log, err)
	}
}

func TestRedditReplyHasNoSlot(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	res, err := svc.CreateDraft(ctx, model.DraftRequest{
		Project: "kiran", Platform: "reddit", Text: "I built Kiran for exactly this.",
		ReplyToURL: "https://www.reddit.com/r/hiking/comments/abc/offline_maps/",
	}, ActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := svc.Approve(ctx, res.Post.ID, model.ApproveRequest{Revision: 1, Actor: "telegram:42"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Post.ScheduledAt != "" {
		t.Fatalf("reddit reply got slot %s", approved.Post.ScheduledAt)
	}
	posts, err := svc.ApprovedForPostiz(ctx)
	if err != nil || len(posts) != 0 {
		t.Fatalf("reddit must not reach Postiz: %v %v", posts, err)
	}
}

func TestInbox(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	items := []model.Mention{
		{Platform: "bluesky", Kind: "mention", ExternalID: "at://1", Text: "loving Kiran on my hike"},
		{Platform: "bluesky", Kind: "reply", ExternalID: "at://2", Text: "what app is this?"},
	}
	res, err := svc.ImportMentions(ctx, items)
	if err != nil || res.Inserted != 2 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	res, err = svc.ImportMentions(ctx, items)
	if err != nil || res.Inserted != 0 {
		t.Fatalf("re-import = %+v, %v", res, err)
	}

	claimed, err := svc.ClaimNewMentions(ctx)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if claimed[0].Project != "kiran" && claimed[1].Project != "kiran" {
		t.Errorf("mention naming Kiran was not mapped: %+v", claimed)
	}
	again, _ := svc.ClaimNewMentions(ctx)
	if len(again) != 0 {
		t.Fatalf("second claim returned %d", len(again))
	}

	rev, err := svc.ImportReviews(ctx, []model.Review{
		{Store: "appstore", AppID: "1", ExternalID: "r1", Rating: 2, Text: "crashes on launch", PostedAt: "2026-09-13T20:00:00-07:00"},
	})
	if err != nil || rev.Inserted != 1 {
		t.Fatalf("review import = %+v, %v", rev, err)
	}
	pending := false
	reviews, _ := svc.ListReviews(ctx, &pending, 10)
	if len(reviews) != 1 || reviews[0].Project != "kiran" || reviews[0].PostedAt != "2026-09-14T03:00:00Z" {
		t.Fatalf("reviews = %+v", reviews)
	}

	digest, err := svc.Digest(ctx, time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(digest.Mentions) != 2 || len(digest.Reviews) != 1 || digest.Empty() {
		t.Fatalf("digest = %+v", digest)
	}
}

func TestAssetPathStaysInsideRoot(t *testing.T) {
	svc, _ := newTestService(t)
	p, err := svc.AssetPath("../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, svc.cfg.AssetsDir+string(filepath.Separator)) {
		t.Fatalf("escaped the assets root: %s", p)
	}
}

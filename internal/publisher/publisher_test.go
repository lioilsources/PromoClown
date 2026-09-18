package publisher

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/postiz"
)

// fakePostiz keeps just enough state to exercise create → poll → published.
type fakePostiz struct {
	mu       sync.Mutex
	created  []postiz.CreateRequest
	posts    []postiz.Post
	uploads  int
	failWith int
}

func (f *fakePostiz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "pz-key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/public/v1/integrations":
		json.NewEncoder(w).Encode([]postiz.Integration{{ID: "int-bsky", Identifier: "bluesky"}, {ID: "int-x", Identifier: "x"}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/public/v1/upload":
		if _, _, err := r.FormFile("file"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.uploads++
		json.NewEncoder(w).Encode(postiz.Media{ID: "m1", Path: "https://postiz.example/uploads/a.png"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/public/v1/posts":
		if f.failWith != 0 {
			w.WriteHeader(f.failWith)
			io.WriteString(w, `{"message":"nope"}`)
			return
		}
		var cr postiz.CreateRequest
		json.NewDecoder(r.Body).Decode(&cr)
		f.created = append(f.created, cr)
		p := postiz.Post{ID: "pz-1", Content: "<p>" + cr.Posts[0].Value[0].Content + "</p>", PublishDate: cr.Date, State: postiz.StateQueue}
		p.Integration.ID = cr.Posts[0].Integration.ID
		f.posts = append(f.posts, p)
		json.NewEncoder(w).Encode([]postiz.Created{{PostID: "pz-1", Integration: cr.Posts[0].Integration.ID}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/public/v1/posts":
		json.NewEncoder(w).Encode(map[string]any{"posts": f.posts})
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T) (*core.Service, *Publisher, *fakePostiz, int64) {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	assets := t.TempDir()
	os.MkdirAll(filepath.Join(assets, "kiran"), 0o755)
	os.WriteFile(filepath.Join(assets, "kiran", "shot.png"), []byte("\x89PNG"), 0o644)

	svc := core.New(conn, core.Config{
		AssetsDir: assets,
		Schedule:  core.Schedule{Loc: time.UTC, WindowStart: 0, WindowEnd: 24 * time.Hour, Lead: time.Minute},
	})
	ctx := context.Background()
	if _, err := svc.UpsertProject(ctx, model.Project{Slug: "kiran", Name: "Kiran"}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.CreateDraft(ctx, model.DraftRequest{
		Project: "kiran", Platform: "bluesky", Text: "Kiran 1.2 is out", MediaPaths: []string{"kiran/shot.png"},
	}, core.ActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, res.Post.ID, model.ApproveRequest{Revision: 1, Actor: "test"}); err != nil {
		t.Fatal(err)
	}

	fake := &fakePostiz{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	pub := New(svc, postiz.New(srv.URL+"/api/public/v1", "pz-key"), Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return svc, pub, fake, res.Post.ID
}

func TestPublishAndSync(t *testing.T) {
	ctx := context.Background()
	svc, pub, fake, id := setup(t)

	if err := pub.PublishApproved(ctx); err != nil {
		t.Fatal(err)
	}
	post, _ := svc.GetPost(ctx, id)
	if post.Status != model.StatusScheduled || post.PostizPostID != "pz-1" {
		t.Fatalf("after publish: %+v", post)
	}
	if len(fake.created) != 1 || fake.uploads != 1 {
		t.Fatalf("created=%d uploads=%d", len(fake.created), fake.uploads)
	}
	entry := fake.created[0].Posts[0]
	if entry.Integration.ID != "int-bsky" || entry.Settings["__type"] != "bluesky" || len(entry.Value[0].Image) != 1 {
		t.Fatalf("create request = %+v", fake.created[0])
	}

	// A second pass must not create it again.
	if err := pub.PublishApproved(ctx); err != nil || len(fake.created) != 1 {
		t.Fatalf("second pass created %d, err %v", len(fake.created), err)
	}

	fake.mu.Lock()
	fake.posts[0].State = postiz.StatePublished
	fake.posts[0].ReleaseURL = "https://bsky.app/profile/did:plc:x/post/abc"
	fake.mu.Unlock()
	if err := pub.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	post, _ = svc.GetPost(ctx, id)
	if post.Status != model.StatusPublished || !strings.HasPrefix(post.ReleaseURL, "https://bsky.app/") {
		t.Fatalf("after sync: %+v", post)
	}
}

func TestAdoptsPostCreatedBeforeCrash(t *testing.T) {
	ctx := context.Background()
	svc, pub, fake, id := setup(t)
	post, _ := svc.GetPost(ctx, id)
	at, _ := db.ParseTime(post.ScheduledAt)
	existing := postiz.Post{ID: "pz-old", Content: "<p>Kiran 1.2 is out</p>", PublishDate: postiz.FormatDate(at), State: postiz.StateQueue}
	existing.Integration.ID = "int-bsky"
	fake.posts = append(fake.posts, existing)

	if err := pub.PublishApproved(ctx); err != nil {
		t.Fatal(err)
	}
	post, _ = svc.GetPost(ctx, id)
	if post.PostizPostID != "pz-old" || len(fake.created) != 0 {
		t.Fatalf("should adopt existing post: %+v, created %d", post, len(fake.created))
	}
}

func TestPermanentErrorFailsPost(t *testing.T) {
	ctx := context.Background()
	svc, pub, fake, id := setup(t)
	fake.failWith = http.StatusBadRequest
	if err := pub.PublishApproved(ctx); err != nil {
		t.Fatal(err)
	}
	post, _ := svc.GetPost(ctx, id)
	if post.Status != model.StatusFailed || !strings.Contains(post.Error, "400") {
		t.Fatalf("post = %+v", post)
	}
}

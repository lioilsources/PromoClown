package core

import (
	"context"
	"errors"
	"testing"

	"github.com/lioilsources/promoclown/internal/model"
)

func TestNormalizeCredit(t *testing.T) {
	for in, want := range map[string]string{
		"@artist":               "@artist",
		"artist":                "@artist",
		"  @artist  ":           "@artist",
		"https://x.com/artist":  "@artist",
		"x.com/artist":          "@artist",
		"@":                     "",
		"":                      "",
		"https://x.com/artist/": "",
	} {
		if got := NormalizeCredit(in); got != want {
			t.Errorf("NormalizeCredit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTributeLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	queued, err := svc.EnqueueTribute(ctx, model.TributeRequest{
		Project: "kiran", Credit: "artist", SourcePath: "kiran/shot.png",
		Note: "fanart", Actor: "telegram:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Credit != "@artist" || queued.Status != "queued" {
		t.Fatalf("enqueued %+v", queued)
	}
	if n, err := svc.QueuedTributes(ctx); err != nil || n != 1 {
		t.Fatalf("queued = %d, %v", n, err)
	}

	claimed, err := svc.ClaimTribute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != queued.ID || claimed.Status != "running" {
		t.Fatalf("claimed %+v", claimed)
	}
	// The status change is the lock: nothing is left to claim.
	if _, err := svc.ClaimTribute(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim: %v, want ErrNotFound", err)
	}

	draft, err := svc.CreateDraft(ctx, model.DraftRequest{
		Project: "kiran", Platform: model.PlatformX, Text: "Tribute to @artist",
		MediaPaths: []string{"kiran/shot.png"},
	}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	done, err := svc.FinishTribute(ctx, claimed.ID, model.TributeDoneRequest{
		PostID: draft.Post.ID, Styles: []string{"monet", "schiele"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "drafted" || done.PostID != draft.Post.ID || len(done.Styles) != 2 {
		t.Fatalf("finished %+v", done)
	}
	if _, err := svc.FinishTribute(ctx, claimed.ID, model.TributeDoneRequest{PostID: draft.Post.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("finishing twice: %v, want ErrConflict", err)
	}
}

func TestTributeFailureRequeues(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	if _, err := svc.EnqueueTribute(ctx, model.TributeRequest{
		Project: "kiran", Credit: "@artist", SourcePath: "kiran/shot.png",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTribute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := svc.FinishTribute(ctx, claimed.ID, model.TributeDoneRequest{Error: "no face detected"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.Error != "no face detected" {
		t.Fatalf("failed %+v", failed)
	}
	back, err := svc.RetryTribute(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != "queued" || back.Error != "" {
		t.Fatalf("requeued %+v", back)
	}
	if _, err := svc.ClaimTribute(ctx); err != nil {
		t.Fatalf("requeued tribute is not claimable: %v", err)
	}
}

func TestEnqueueTributeRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	for name, req := range map[string]model.TributeRequest{
		"no credit":    {Project: "kiran", SourcePath: "kiran/shot.png"},
		"bad credit":   {Project: "kiran", Credit: "@not a handle", SourcePath: "kiran/shot.png"},
		"no source":    {Project: "kiran", Credit: "@artist"},
		"missing file": {Project: "kiran", Credit: "@artist", SourcePath: "kiran/nope.png"},
		"escaping":     {Project: "kiran", Credit: "@artist", SourcePath: "../../etc/passwd"},
	} {
		t.Run(name, func(t *testing.T) {
			var ve *ValidationError
			if _, err := svc.EnqueueTribute(ctx, req); !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a validation error", err)
			}
		})
	}

	if _, err := svc.EnqueueTribute(ctx, model.TributeRequest{
		Project: "nope", Credit: "@artist", SourcePath: "kiran/shot.png",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v, want ErrNotFound", err)
	}
}

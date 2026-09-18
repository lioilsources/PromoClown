// Package publisher moves approved posts into Postiz and reads their fate
// back. It is deterministic code with no model in the loop: the only way a
// post reaches it is a human approval recorded by promo-api.
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/postiz"
)

type Config struct {
	// Integrations maps "platform" or "platform:account" → Postiz integration
	// id. Missing keys are resolved from GET /integrations by provider
	// identifier, and by channel name when the post names an account.
	Integrations   map[string]string
	YouTubePrivacy string // public | unlisted | private
	XWhoCanReply   string // everyone | following | mentionedUsers | subscribers | verified
	Interval       time.Duration
	SyncInterval   time.Duration
	// MaxAttempts before a transient Postiz error fails the post for good.
	MaxAttempts int
}

type Publisher struct {
	svc *core.Service
	pz  *postiz.Client
	cfg Config
	log *slog.Logger

	mu       sync.Mutex // one run at a time: ticker, webhook and nudges overlap
	attempts map[int64]int
	nudge    chan struct{}
	now      func() time.Time
}

func New(svc *core.Service, pz *postiz.Client, cfg Config, log *slog.Logger) *Publisher {
	if cfg.YouTubePrivacy == "" {
		cfg.YouTubePrivacy = "public"
	}
	if cfg.XWhoCanReply == "" {
		cfg.XWhoCanReply = "everyone"
	}
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = 10 * time.Minute
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	return &Publisher{
		svc: svc, pz: pz, cfg: cfg, log: log,
		attempts: map[int64]int{}, nudge: make(chan struct{}, 1), now: time.Now,
	}
}

// Nudge asks for a publish pass now instead of at the next tick, so an
// approval shows up in the Postiz calendar within seconds.
func (p *Publisher) Nudge() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

func (p *Publisher) Run(ctx context.Context) {
	publish := time.NewTicker(p.cfg.Interval)
	sync := time.NewTicker(p.cfg.SyncInterval)
	defer publish.Stop()
	defer sync.Stop()
	p.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-publish.C:
			p.runPublish(ctx)
		case <-p.nudge:
			p.runPublish(ctx)
		case <-sync.C:
			p.runSync(ctx)
		}
	}
}

func (p *Publisher) runOnce(ctx context.Context) {
	p.runPublish(ctx)
	p.runSync(ctx)
}

func (p *Publisher) runPublish(ctx context.Context) {
	if err := p.PublishApproved(ctx); err != nil {
		p.log.Error("publish pass failed", "err", err)
	}
}

func (p *Publisher) runSync(ctx context.Context) {
	if err := p.Sync(ctx); err != nil {
		p.log.Error("sync pass failed", "err", err)
	}
}

// HandleWebhook treats a Postiz webhook as a hint to sync. Postiz cannot sign
// webhooks, so nothing in the body is trusted; state is re-read from the API.
func (p *Publisher) HandleWebhook(ctx context.Context, body []byte) {
	var items []postiz.Post
	if err := json.Unmarshal(body, &items); err != nil {
		p.log.Warn("postiz webhook: unreadable body", "err", err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	p.log.Info("postiz webhook", "posts", strings.Join(ids, ","))
	p.runSync(ctx)
}

// integrationFor resolves the Postiz channel a post goes out from. A post that
// names an account (its project's postiz_accounts) must match that channel;
// one that does not takes the platform's only channel, as before.
func (p *Publisher) integrationFor(ctx context.Context, platform, account string) (string, error) {
	key := platform
	if account != "" {
		key = platform + ":" + account
	}
	if id := p.cfg.Integrations[key]; id != "" {
		return id, nil
	}
	list, err := p.pz.Integrations(ctx)
	if err != nil {
		return "", err
	}
	var found []postiz.Integration
	for _, in := range list {
		if in.Identifier != platform || in.Disabled {
			continue
		}
		if account != "" && !isAccount(in, account) {
			continue
		}
		found = append(found, in)
	}
	switch len(found) {
	case 0:
		if account != "" {
			return "", fmt.Errorf("no enabled %s channel %q in Postiz; connect it, or map it with POSTIZ_INTEGRATIONS=%s=<id>",
				platform, account, key)
		}
		return "", fmt.Errorf("no enabled %s channel in Postiz; connect it first", platform)
	case 1:
		if p.cfg.Integrations == nil {
			p.cfg.Integrations = map[string]string{}
		}
		p.cfg.Integrations[key] = found[0].ID
		return found[0].ID, nil
	}
	return "", fmt.Errorf("%d %s channels in Postiz match %q; pick one with POSTIZ_INTEGRATIONS=%s=<id>",
		len(found), platform, account, key)
}

// isAccount matches the project's account against what Postiz knows about a
// channel: its id, or its name with any leading @ ignored.
func isAccount(in postiz.Integration, account string) bool {
	want := strings.ToLower(strings.TrimPrefix(account, "@"))
	return in.ID == account ||
		strings.ToLower(strings.TrimPrefix(in.Name, "@")) == want ||
		strings.ToLower(strings.TrimPrefix(in.Profile, "@")) == want
}

func (p *Publisher) settings(post model.Post) map[string]any {
	switch post.Platform {
	case model.PlatformX:
		return map[string]any{"__type": "x", "who_can_reply_post": p.cfg.XWhoCanReply, "community": "",
			"made_with_ai": false, "paid_partnership": false}
	case model.PlatformYouTube:
		return map[string]any{"__type": "youtube", "title": post.Title, "type": p.cfg.YouTubePrivacy,
			"selfDeclaredMadeForKids": "no", "tags": []postiz.Tag{}}
	}
	return map[string]any{"__type": post.Platform}
}

// PublishApproved hands every approved post (except Reddit, which is posted
// by hand) to Postiz as a scheduled post.
func (p *Publisher) PublishApproved(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	posts, err := p.svc.ApprovedForPostiz(ctx)
	if err != nil {
		return err
	}
	for _, post := range posts {
		if err := p.publish(ctx, post); err != nil {
			p.fail(ctx, post, err)
			continue
		}
		delete(p.attempts, post.ID)
	}
	return nil
}

func (p *Publisher) fail(ctx context.Context, post model.Post, err error) {
	p.attempts[post.ID]++
	var pe *postiz.Error
	permanent := errors.As(err, &pe) && pe.Permanent()
	p.log.Warn("publish failed", "post", post.ID, "attempt", p.attempts[post.ID], "permanent", permanent, "err", err)
	if permanent || p.attempts[post.ID] >= p.cfg.MaxAttempts {
		delete(p.attempts, post.ID)
		if ferr := p.svc.MarkFailed(ctx, post.ID, err.Error(), core.ActorPublisher); ferr != nil {
			p.log.Error("mark failed", "post", post.ID, "err", ferr)
		}
		return
	}
	if serr := p.svc.SetPostError(ctx, post.ID, err.Error()); serr != nil {
		p.log.Error("set post error", "post", post.ID, "err", serr)
	}
}

func (p *Publisher) publish(ctx context.Context, post model.Post) error {
	integration, err := p.integrationFor(ctx, post.Platform, post.Account)
	if err != nil {
		return err
	}

	at := p.now().Add(2 * time.Minute)
	if post.ScheduledAt != "" {
		if t, err := db.ParseTime(post.ScheduledAt); err == nil && t.After(at) {
			at = t
		}
	}

	// A crash between creating the post and recording it would otherwise
	// create it twice on the next pass.
	if existing, err := p.findExisting(ctx, integration, post, at); err != nil {
		return err
	} else if existing != nil {
		p.log.Info("post already in postiz, adopting", "post", post.ID, "postiz", existing.ID)
		return p.svc.MarkScheduled(ctx, post.ID, existing.ID, existing.Group, at)
	}

	media := make([]postiz.Media, 0, len(post.MediaPaths))
	for _, rel := range post.MediaPaths {
		m, err := p.upload(ctx, rel)
		if err != nil {
			return fmt.Errorf("upload %s: %w", rel, err)
		}
		media = append(media, m)
	}

	created, err := p.pz.CreatePost(ctx, postiz.CreateRequest{
		Type:      "schedule",
		Date:      postiz.FormatDate(at),
		ShortLink: false,
		Tags:      []postiz.Tag{},
		Posts: []postiz.Entry{{
			Integration: postiz.IntegrationRef{ID: integration},
			Value:       []postiz.Value{{Content: post.Text, Image: media}},
			Settings:    p.settings(post),
		}},
	})
	if err != nil {
		return err
	}
	if len(created) == 0 || created[0].PostID == "" {
		return errors.New("postiz returned no post id")
	}
	p.log.Info("scheduled in postiz", "post", post.ID, "postiz", created[0].PostID, "at", at.Format(time.RFC3339))
	return p.svc.MarkScheduled(ctx, post.ID, created[0].PostID, "", at)
}

func (p *Publisher) upload(ctx context.Context, rel string) (postiz.Media, error) {
	path, err := p.svc.AssetPath(rel)
	if err != nil {
		return postiz.Media{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return postiz.Media{}, err
	}
	defer f.Close()
	return p.pz.Upload(ctx, filepath.Base(path), f)
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

// sameText compares our plain text with what Postiz stored, which may have
// been wrapped in HTML by its editor.
func sameText(ours, theirs string) bool {
	norm := func(s string) string {
		s = tagRe.ReplaceAllString(s, " ")
		return strings.Join(strings.Fields(s), " ")
	}
	return norm(ours) == norm(theirs)
}

func (p *Publisher) findExisting(ctx context.Context, integration string, post model.Post, at time.Time) (*postiz.Post, error) {
	list, err := p.pz.Posts(ctx, at.Add(-24*time.Hour), at.Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Integration.ID == integration && sameText(post.Text, list[i].Content) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// Sync reads the state of every scheduled post back from Postiz.
func (p *Publisher) Sync(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	scheduled, err := p.svc.ScheduledPosts(ctx)
	if err != nil || len(scheduled) == 0 {
		return err
	}
	now := p.now()
	remote, err := p.pz.Posts(ctx, now.Add(-14*24*time.Hour), now.Add(200*24*time.Hour))
	if err != nil {
		return err
	}
	byID := make(map[string]postiz.Post, len(remote))
	for _, r := range remote {
		byID[r.ID] = r
	}

	for _, post := range scheduled {
		r, ok := byID[post.PostizPostID]
		if !ok {
			// Deleted in the Postiz UI, most likely. Give clocks and slow
			// queues an hour before calling it.
			if at, err := db.ParseTime(post.ScheduledAt); err == nil && now.Sub(at) > time.Hour {
				p.report(post, p.svc.MarkFailed(ctx, post.ID, "post no longer exists in Postiz", core.ActorPostiz))
			}
			continue
		}
		switch r.State {
		case postiz.StatePublished:
			at, err := time.Parse(time.RFC3339, r.PublishDate)
			if err != nil {
				at = now
			}
			_, err = p.svc.MarkPublished(ctx, post.ID, r.ReleaseURL, at, core.ActorPostiz)
			p.report(post, err)
		case postiz.StateError:
			p.report(post, p.svc.MarkFailed(ctx, post.ID, "Postiz reported ERROR, see its calendar for details", core.ActorPostiz))
		}
	}
	return nil
}

func (p *Publisher) report(post model.Post, err error) {
	if err != nil {
		p.log.Error("sync update failed", "post", post.ID, "err", err)
	}
}

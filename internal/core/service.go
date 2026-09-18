// Package core is promo-api's domain layer: projects, the post lifecycle
// (draft → approved → scheduled → published), and the inbox of mentions and
// reviews. HTTP handlers, the publisher and the approval bot all go through it.
package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// ValidationError lists every reason a request was refused, so the agent can
// fix them all in one retry.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string { return strings.Join(e.Problems, "; ") }

func invalid(problems ...string) error { return &ValidationError{Problems: problems} }

// Actors. Anything else is a human, recorded as "telegram:<user id>" or "cli".
const (
	ActorAgent     = "agent"
	ActorPublisher = "publisher"
	ActorPostiz    = "postiz"
)

// Events the approval bot reports on its own. Approvals, rejections and edits
// are answered inline by whoever made them, so they are born notified.
var notifyActions = map[string]bool{"scheduled": true, "published": true, "failed": true}

type Config struct {
	AssetsDir       string
	Schedule        Schedule
	DuplicateWindow time.Duration
	Now             func() time.Time
}

type Service struct {
	conn *sql.DB
	q    *db.Queries
	cfg  Config
}

func New(conn *sql.DB, cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DuplicateWindow == 0 {
		cfg.DuplicateWindow = 30 * 24 * time.Hour
	}
	if cfg.Schedule.Loc == nil {
		cfg.Schedule.Loc = time.UTC
	}
	if cfg.Schedule.WindowEnd == 0 {
		cfg.Schedule.WindowEnd = 24 * time.Hour
	}
	return &Service{conn: conn, q: db.New(conn), cfg: cfg}
}

// Location is the timezone for posting slots and local times typed by humans.
func (s *Service) Location() *time.Location { return s.cfg.Schedule.Loc }

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

func (s *Service) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func wrapNotFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return err
}

// ParseWhen accepts RFC 3339 or a local "2026-09-14T10:00" / "2026-09-14 10:00".
func ParseWhen(v string, loc *time.Location) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read time %q, use 2026-09-14T10:00 (local) or RFC 3339", v)
}

// Projects ---------------------------------------------------------------------

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Service) ListProjects(ctx context.Context, status string) ([]model.Project, error) {
	rows, err := s.q.ListProjects(ctx, nullString(status))
	if err != nil {
		return nil, err
	}
	out := make([]model.Project, len(rows))
	for i, r := range rows {
		out[i] = projectModel(r)
	}
	return out, nil
}

func (s *Service) GetProject(ctx context.Context, slug string) (model.Project, error) {
	r, err := s.q.GetProjectBySlug(ctx, slug)
	if err != nil {
		return model.Project{}, wrapNotFound(err, "project "+slug)
	}
	return projectModel(r), nil
}

func (s *Service) UpsertProject(ctx context.Context, p model.Project) (model.Project, error) {
	var problems []string
	if !slugRe.MatchString(p.Slug) {
		problems = append(problems, fmt.Sprintf("slug %q must be lowercase letters, digits and dashes", p.Slug))
	}
	if strings.TrimSpace(p.Name) == "" {
		problems = append(problems, "name is required")
	}
	if p.Status == "" {
		p.Status = "active"
	}
	if p.Status != "active" && p.Status != "paused" && p.Status != "archived" {
		problems = append(problems, "status must be active, paused or archived")
	}
	if p.AssetsDir == "" {
		p.AssetsDir = p.Slug
	}
	if p.DailyCap == 0 {
		p.DailyCap = DefaultLimits.PerDay
	}
	if p.MinDaysBetween == 0 {
		p.MinDaysBetween = int(DefaultLimits.Gap.Hours() / 24)
	}
	if p.DailyCap < 1 {
		problems = append(problems, "daily_cap must be at least 1")
	}
	if p.MinDaysBetween < 0 {
		problems = append(problems, "min_days_between cannot be negative")
	}
	for platform := range p.PostizAccounts {
		if !contains(model.Platforms, platform) {
			problems = append(problems, fmt.Sprintf("postiz_accounts has unknown platform %q", platform))
		}
	}
	if len(problems) > 0 {
		return model.Project{}, invalid(problems...)
	}
	r, err := s.q.UpsertProject(ctx, db.UpsertProjectParams{
		Slug:            p.Slug,
		Name:            strings.TrimSpace(p.Name),
		Tagline:         p.Tagline,
		Audience:        p.Audience,
		Tags:            strings.Join(trimAll(p.Tags), ","),
		Hooks:           strings.Join(trimAll(p.Hooks), "\n"),
		ForbiddenClaims: strings.Join(trimAll(p.ForbiddenClaims), "\n"),
		WebsiteUrl:      p.WebsiteURL,
		StoreIosUrl:     p.StoreIOSURL,
		StoreAndroidUrl: p.StoreAndroidURL,
		IosAppID:        p.IOSAppID,
		AndroidPackage:  p.AndroidPackage,
		AssetsDir:       p.AssetsDir,
		PostizAccounts:  formatAccounts(p.PostizAccounts),
		DailyCap:        int64(p.DailyCap),
		MinDaysBetween:  int64(p.MinDaysBetween),
		Status:          p.Status,
		Now:             db.FormatTime(s.now()),
	})
	if err != nil {
		return model.Project{}, err
	}
	return projectModel(r), nil
}

// Assets -----------------------------------------------------------------------

// AssetPath resolves a media path relative to the assets root. Cleaning it as
// an absolute path first means "../" can never climb out of the root.
func (s *Service) AssetPath(rel string) (string, error) {
	if s.cfg.AssetsDir == "" {
		return "", errors.New("assets are not configured (PROMO_ASSETS_DIR)")
	}
	clean := filepath.Clean("/" + filepath.ToSlash(rel))
	if clean == "/" {
		return "", fmt.Errorf("empty media path")
	}
	return filepath.Join(s.cfg.AssetsDir, clean), nil
}

// MediaKind classifies a file by extension into what Postiz accepts.
func MediaKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".mp4":
		return "video"
	}
	return "other"
}

func (s *Service) checkMedia(platform, rel string) string {
	path, err := s.AssetPath(rel)
	if err != nil {
		return err.Error()
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return fmt.Sprintf("media %s not found (see: promo projects assets <slug>)", rel)
	}
	kind := MediaKind(rel)
	switch {
	case kind == "other":
		return fmt.Sprintf("media %s must be png, jpg, gif, webp or mp4", rel)
	case platform == model.PlatformYouTube && kind != "video":
		return "youtube media must be an mp4 video"
	case kind == "image" && fi.Size() > 10<<20:
		return fmt.Sprintf("image %s is larger than 10 MB", rel)
	case kind == "video" && fi.Size() > 1<<30:
		return fmt.Sprintf("video %s is larger than 1 GB", rel)
	}
	return ""
}

func (s *Service) ListAssets(ctx context.Context, slug string) ([]model.Asset, error) {
	p, err := s.q.GetProjectBySlug(ctx, slug)
	if err != nil {
		return nil, wrapNotFound(err, "project "+slug)
	}
	root, err := s.AssetPath(p.AssetsDir)
	if err != nil {
		return nil, err
	}
	out := []model.Asset{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && path != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || len(out) >= 500 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(s.cfg.AssetsDir, path)
		out = append(out, model.Asset{Path: filepath.ToSlash(rel), Kind: MediaKind(path), Size: info.Size()})
		return nil
	})
	return out, err
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SaveUpload stores a file under <project assets>/uploads/ and returns the
// media path to draft with.
func (s *Service) SaveUpload(ctx context.Context, slug, filename string, r io.Reader) (model.Asset, error) {
	p, err := s.q.GetProjectBySlug(ctx, slug)
	if err != nil {
		return model.Asset{}, wrapNotFound(err, "project "+slug)
	}
	name := unsafeName.ReplaceAllString(filepath.Base(filename), "-")
	kind := MediaKind(name)
	if kind == "other" {
		return model.Asset{}, invalid("upload must be png, jpg, gif, webp or mp4")
	}
	rel := filepath.ToSlash(filepath.Join(p.AssetsDir, "uploads", s.now().Format("20060102-150405")+"-"+name))
	dest, err := s.AssetPath(rel)
	if err != nil {
		return model.Asset{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return model.Asset{}, err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return model.Asset{}, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return model.Asset{}, err
	}
	return model.Asset{Path: rel, Kind: kind, Size: n}, nil
}

// Posts ------------------------------------------------------------------------

type PostFilter struct {
	Status   string
	Project  string
	Platform string
	Limit    int
}

func (s *Service) slugs(ctx context.Context, q *db.Queries) (map[int64]string, error) {
	rows, err := q.ListProjects(ctx, sql.NullString{})
	if err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(rows))
	for _, r := range rows {
		m[r.ID] = r.Slug
	}
	return m, nil
}

func (s *Service) postModels(ctx context.Context, rows []db.Post) ([]model.Post, error) {
	slugs, err := s.slugs(ctx, s.q)
	if err != nil {
		return nil, err
	}
	out := make([]model.Post, len(rows))
	for i, r := range rows {
		out[i] = postModel(r, slugs[r.ProjectID])
	}
	return out, nil
}

func (s *Service) GetPost(ctx context.Context, id int64) (model.Post, error) {
	r, err := s.q.GetPost(ctx, id)
	if err != nil {
		return model.Post{}, wrapNotFound(err, fmt.Sprintf("post #%d", id))
	}
	posts, err := s.postModels(ctx, []db.Post{r})
	if err != nil {
		return model.Post{}, err
	}
	return posts[0], nil
}

func (s *Service) ListPosts(ctx context.Context, f PostFilter) ([]model.Post, error) {
	params := db.ListPostsParams{Status: nullString(f.Status), Platform: nullString(f.Platform), Limit: int64(f.Limit)}
	if params.Limit <= 0 || params.Limit > 500 {
		params.Limit = 100
	}
	if f.Project != "" {
		p, err := s.q.GetProjectBySlug(ctx, f.Project)
		if err != nil {
			return nil, wrapNotFound(err, "project "+f.Project)
		}
		params.ProjectID = sql.NullInt64{Int64: p.ID, Valid: true}
	}
	rows, err := s.q.ListPosts(ctx, params)
	if err != nil {
		return nil, err
	}
	return s.postModels(ctx, rows)
}

// PostLog is what was published or committed since the given time.
func (s *Service) PostLog(ctx context.Context, since time.Time) ([]model.Post, error) {
	rows, err := s.q.PostLog(ctx, nullString(db.FormatTime(since)))
	if err != nil {
		return nil, err
	}
	return s.postModels(ctx, rows)
}

func defaultKind(platform string) string {
	switch platform {
	case model.PlatformYouTube:
		return "short"
	case model.PlatformReddit:
		return "reply"
	}
	return "post"
}

// check validates content against the rules and recent posts. The agent must
// satisfy every rule; a human editing a draft may repeat old wording.
func (s *Service) check(ctx context.Context, q *db.Queries, proj db.Project, req model.DraftRequest, selfID int64, strict bool) (errs, warns []string, err error) {
	errs, warns = ValidateDraft(projectModel(proj), req)
	for _, m := range req.MediaPaths {
		if problem := s.checkMedia(req.Platform, m); problem != "" {
			errs = append(errs, problem)
		}
	}
	recent, err := q.PostsCreatedSince(ctx, db.FormatTime(s.now().Add(-s.cfg.DuplicateWindow)))
	if err != nil {
		return nil, nil, err
	}
	others := make([]model.Post, 0, len(recent))
	for _, r := range recent {
		if r.ID != selfID {
			others = append(others, model.Post{ID: r.ID, Platform: r.Platform, Text: r.Text})
		}
	}
	same, other := FindDuplicates(req.Text, req.Platform, others)
	for _, d := range same {
		msg := fmt.Sprintf("text repeats #%d on %s from the last 30 days (%.0f%% similar)", d.PostID, d.Platform, d.Score*100)
		if strict {
			errs = append(errs, msg)
		} else {
			warns = append(warns, msg)
		}
	}
	for _, d := range other {
		warns = append(warns, fmt.Sprintf("same text as #%d on %s", d.PostID, d.Platform))
	}
	return errs, warns, nil
}

func (s *Service) CreateDraft(ctx context.Context, req model.DraftRequest, actor string) (model.PostResult, error) {
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if req.Kind == "" {
		req.Kind = defaultKind(req.Platform)
	}
	req.Text = strings.TrimSpace(req.Text)
	req.Title = strings.TrimSpace(req.Title)
	req.MediaPaths = trimAll(req.MediaPaths)
	req.ReplyToURL = strings.TrimSpace(req.ReplyToURL)

	var res model.PostResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		proj, err := q.GetProjectBySlug(ctx, req.Project)
		if err != nil {
			return wrapNotFound(err, "project "+req.Project)
		}
		if proj.Status != "active" {
			return invalid(fmt.Sprintf("project %s is %s", proj.Slug, proj.Status))
		}
		errs, warns, err := s.check(ctx, q, proj, req, 0, actor == ActorAgent)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return invalid(errs...)
		}
		now := db.FormatTime(s.now())
		row, err := q.CreatePost(ctx, db.CreatePostParams{
			ProjectID:  proj.ID,
			Platform:   req.Platform,
			Account:    accountFor(proj, req.Platform),
			Kind:       req.Kind,
			Title:      req.Title,
			Text:       req.Text,
			MediaPaths: strings.Join(req.MediaPaths, "\n"),
			ReplyToUrl: req.ReplyToURL,
			Warnings:   strings.Join(warns, "\n"),
			CreatedBy:  actor,
			Now:        now,
		})
		if err != nil {
			return err
		}
		if err := addEvent(ctx, q, row.ID, "created", actor, "", now); err != nil {
			return err
		}
		res = model.PostResult{Post: postModel(row, proj.Slug), Warnings: warns}
		return nil
	})
	return res, err
}

// EditDraft changes a draft's content and bumps its revision, which voids any
// approval preview already sent for the old text.
func (s *Service) EditDraft(ctx context.Context, id int64, req model.EditRequest) (model.PostResult, error) {
	var res model.PostResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		post, err := q.GetPost(ctx, id)
		if err != nil {
			return wrapNotFound(err, fmt.Sprintf("post #%d", id))
		}
		if post.Status != model.StatusDraft {
			return fmt.Errorf("post #%d is %s, only drafts can be edited: %w", id, post.Status, ErrConflict)
		}
		project, err := q.GetProjectByID(ctx, post.ProjectID)
		if err != nil {
			return err
		}
		draft := model.DraftRequest{
			Project: project.Slug, Platform: post.Platform, Kind: post.Kind,
			Title: post.Title, Text: post.Text, MediaPaths: splitLines(post.MediaPaths), ReplyToURL: post.ReplyToUrl,
		}
		if req.Text != nil {
			draft.Text = strings.TrimSpace(*req.Text)
		}
		if req.Title != nil {
			draft.Title = strings.TrimSpace(*req.Title)
		}
		if req.MediaPaths != nil {
			draft.MediaPaths = trimAll(*req.MediaPaths)
		}
		errs, warns, err := s.check(ctx, q, project, draft, post.ID, req.Actor == ActorAgent)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return invalid(errs...)
		}
		now := db.FormatTime(s.now())
		row, err := q.UpdateDraftContent(ctx, db.UpdateDraftContentParams{
			ID: id, Text: draft.Text, Title: draft.Title, MediaPaths: strings.Join(draft.MediaPaths, "\n"),
			Warnings: strings.Join(warns, "\n"), Now: now,
		})
		if err != nil {
			return wrapNotFound(err, fmt.Sprintf("draft #%d", id))
		}
		if err := addEvent(ctx, q, id, "edited", req.Actor, fmt.Sprintf("revision %d", row.Revision), now); err != nil {
			return err
		}
		res = model.PostResult{Post: postModel(row, project.Slug), Warnings: warns}
		return nil
	})
	return res, err
}

func (s *Service) committed(ctx context.Context, q *db.Queries, platform, account string, exclude int64) ([]Slot, error) {
	now := s.now()
	rows, err := q.CommittedPostsBetween(ctx, db.CommittedPostsBetweenParams{
		Platform: platform,
		Account:  account,
		From:     nullString(db.FormatTime(now.Add(-8 * 24 * time.Hour))),
		To:       nullString(db.FormatTime(now.Add(200 * 24 * time.Hour))),
	})
	if err != nil {
		return nil, err
	}
	var out []Slot
	for _, r := range rows {
		if r.ID == exclude {
			continue
		}
		v := r.PublishedAt
		if !v.Valid {
			v = r.ScheduledAt
		}
		at, err := db.ParseTime(v.String)
		if err != nil {
			continue
		}
		out = append(out, Slot{PostID: r.ID, ProjectID: r.ProjectID, At: at})
	}
	return out, nil
}

// slotFor picks or checks the publication time of post. Reddit replies are
// posted by hand, so they get no slot.
func (s *Service) slotFor(ctx context.Context, q *db.Queries, post db.Post, requested string) (sql.NullString, []string, error) {
	if post.Platform == model.PlatformReddit {
		return sql.NullString{}, nil, nil
	}
	committed, err := s.committed(ctx, q, post.Platform, post.Account, post.ID)
	if err != nil {
		return sql.NullString{}, nil, err
	}
	proj, err := q.GetProjectByID(ctx, post.ProjectID)
	if err != nil {
		return sql.NullString{}, nil, err
	}
	lim := limitsOf(proj)
	sched := s.cfg.Schedule
	if requested == "" {
		at, err := sched.NextSlot(s.now(), post.ProjectID, lim, committed)
		if err != nil {
			return sql.NullString{}, nil, invalid(err.Error())
		}
		return nullString(db.FormatTime(at)), nil, nil
	}
	at, err := ParseWhen(requested, sched.Loc)
	if err != nil {
		return sql.NullString{}, nil, invalid(err.Error())
	}
	if at.Before(s.now().Add(sched.Lead)) {
		return sql.NullString{}, nil, invalid(fmt.Sprintf("time must be at least %s from now", sched.Lead))
	}
	// A human naming a time overrides the frequency rules; say what it breaks.
	warns := sched.Violations(at, post.ProjectID, lim, committed)
	if sched.OutsideWindow(at) {
		warns = append(warns, "outside the daily posting window")
	}
	return nullString(db.FormatTime(at)), warns, nil
}

func (s *Service) Approve(ctx context.Context, id int64, req model.ApproveRequest) (model.PostResult, error) {
	var res model.PostResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		post, err := q.GetPost(ctx, id)
		if err != nil {
			return wrapNotFound(err, fmt.Sprintf("post #%d", id))
		}
		if post.Status != model.StatusDraft {
			return fmt.Errorf("post #%d is %s, not a draft: %w", id, post.Status, ErrConflict)
		}
		if req.Revision != post.Revision {
			return fmt.Errorf("post #%d changed since that preview (you saw revision %d, it is now %d): %w",
				id, req.Revision, post.Revision, ErrConflict)
		}
		at, warns, err := s.slotFor(ctx, q, post, req.ScheduledAt)
		if err != nil {
			return err
		}
		now := db.FormatTime(s.now())
		row, err := q.ApprovePost(ctx, db.ApprovePostParams{
			ID: id, Revision: req.Revision, Actor: req.Actor, ScheduledAt: at, Now: nullString(now),
		})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("post #%d changed while approving: %w", id, ErrConflict)
		}
		if err != nil {
			return err
		}
		detail := strings.TrimSpace(at.String + " " + strings.Join(warns, "; "))
		if err := addEvent(ctx, q, id, "approved", req.Actor, detail, now); err != nil {
			return err
		}
		slugs, err := s.slugs(ctx, q)
		if err != nil {
			return err
		}
		res = model.PostResult{Post: postModel(row, slugs[row.ProjectID]), Warnings: warns}
		return nil
	})
	return res, err
}

func (s *Service) Reject(ctx context.Context, id int64, req model.RejectRequest) (model.Post, error) {
	var out model.Post
	err := s.inTx(ctx, func(q *db.Queries) error {
		now := db.FormatTime(s.now())
		row, err := q.RejectPost(ctx, db.RejectPostParams{ID: id, Reason: req.Reason, Now: now})
		if errors.Is(err, sql.ErrNoRows) {
			if post, gerr := q.GetPost(ctx, id); gerr == nil {
				return fmt.Errorf("post #%d is %s and can no longer be rejected here (delete it in Postiz): %w", id, post.Status, ErrConflict)
			}
			return fmt.Errorf("post #%d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := addEvent(ctx, q, id, "rejected", req.Actor, req.Reason, now); err != nil {
			return err
		}
		slugs, err := s.slugs(ctx, q)
		out = postModel(row, slugs[row.ProjectID])
		return err
	})
	return out, err
}

// Retry sends a failed post back to the publisher with a fresh slot.
func (s *Service) Retry(ctx context.Context, id int64, req model.RetryRequest) (model.PostResult, error) {
	var res model.PostResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		post, err := q.GetPost(ctx, id)
		if err != nil {
			return wrapNotFound(err, fmt.Sprintf("post #%d", id))
		}
		if post.Status != model.StatusFailed {
			return fmt.Errorf("post #%d is %s, only failed posts can be retried: %w", id, post.Status, ErrConflict)
		}
		at, warns, err := s.slotFor(ctx, q, post, req.ScheduledAt)
		if err != nil {
			return err
		}
		now := db.FormatTime(s.now())
		row, err := q.RetryPost(ctx, db.RetryPostParams{ID: id, ScheduledAt: at, Now: now})
		if err != nil {
			return wrapNotFound(err, fmt.Sprintf("failed post #%d", id))
		}
		if err := addEvent(ctx, q, id, "retried", req.Actor, at.String, now); err != nil {
			return err
		}
		slugs, err := s.slugs(ctx, q)
		res = model.PostResult{Post: postModel(row, slugs[row.ProjectID]), Warnings: warns}
		return err
	})
	return res, err
}

// PostEvents returns the audit trail of one post.
func (s *Service) PostEvents(ctx context.Context, id int64) ([]model.PostEvent, error) {
	rows, err := s.q.ListPostEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]model.PostEvent, len(rows))
	for i, r := range rows {
		out[i] = eventModel(r)
	}
	return out, nil
}

// Publisher side ---------------------------------------------------------------

func (s *Service) ApprovedForPostiz(ctx context.Context) ([]model.Post, error) {
	rows, err := s.q.ListApprovedForPostiz(ctx)
	if err != nil {
		return nil, err
	}
	return s.postModels(ctx, rows)
}

func (s *Service) ScheduledPosts(ctx context.Context) ([]model.Post, error) {
	rows, err := s.q.ListScheduled(ctx)
	if err != nil {
		return nil, err
	}
	return s.postModels(ctx, rows)
}

func (s *Service) MarkScheduled(ctx context.Context, id int64, postizID, group string, at time.Time) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		now := db.FormatTime(s.now())
		_, err := q.MarkScheduled(ctx, db.MarkScheduledParams{
			ID: id, PostizPostID: postizID, PostizGroup: group,
			ScheduledAt: nullString(db.FormatTime(at)), Now: now,
		})
		if err != nil {
			return wrapNotFound(err, fmt.Sprintf("approved post #%d", id))
		}
		return addEvent(ctx, q, id, "scheduled", ActorPublisher, db.FormatTime(at), now)
	})
}

// MarkPublished records a post as live. Calling it again for a post that is
// already published is a no-op, so webhooks and polling can both report it.
func (s *Service) MarkPublished(ctx context.Context, id int64, url string, at time.Time, actor string) (model.Post, error) {
	if at.IsZero() {
		at = s.now()
	}
	var out model.Post
	err := s.inTx(ctx, func(q *db.Queries) error {
		now := db.FormatTime(s.now())
		row, err := q.MarkPublished(ctx, db.MarkPublishedParams{
			ID: id, ReleaseUrl: url, PublishedAt: nullString(db.FormatTime(at)), Now: now,
		})
		if errors.Is(err, sql.ErrNoRows) {
			post, gerr := q.GetPost(ctx, id)
			if gerr != nil {
				return wrapNotFound(gerr, fmt.Sprintf("post #%d", id))
			}
			if post.Status == model.StatusPublished {
				row, err = post, nil
			} else {
				return fmt.Errorf("post #%d is %s, cannot be marked published: %w", id, post.Status, ErrConflict)
			}
		} else if err != nil {
			return err
		} else if err := addEvent(ctx, q, id, "published", actor, url, now); err != nil {
			return err
		}
		slugs, err := s.slugs(ctx, q)
		out = postModel(row, slugs[row.ProjectID])
		return err
	})
	return out, err
}

func (s *Service) MarkFailed(ctx context.Context, id int64, reason, actor string) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		now := db.FormatTime(s.now())
		if _, err := q.MarkFailed(ctx, db.MarkFailedParams{ID: id, Error: reason, Now: now}); err != nil {
			return wrapNotFound(err, fmt.Sprintf("post #%d in flight", id))
		}
		return addEvent(ctx, q, id, "failed", actor, reason, now)
	})
}

// SetPostError notes a transient problem without changing state.
func (s *Service) SetPostError(ctx context.Context, id int64, msg string) error {
	return s.q.SetPostError(ctx, db.SetPostErrorParams{ID: id, Error: msg, Now: db.FormatTime(s.now())})
}

func (s *Service) FindByPostizRef(ctx context.Context, ref string) (model.Post, error) {
	r, err := s.q.FindPostByPostizRef(ctx, ref)
	if err != nil {
		return model.Post{}, wrapNotFound(err, "postiz post "+ref)
	}
	posts, err := s.postModels(ctx, []db.Post{r})
	if err != nil {
		return model.Post{}, err
	}
	return posts[0], nil
}

// Approval bot side ------------------------------------------------------------

// DraftsToNotify are drafts whose current revision has no Telegram preview yet.
func (s *Service) DraftsToNotify(ctx context.Context) ([]model.Post, error) {
	rows, err := s.q.ListDraftsToNotify(ctx)
	if err != nil {
		return nil, err
	}
	return s.postModels(ctx, rows)
}

func (s *Service) SetPostNotified(ctx context.Context, id, revision, messageID int64) error {
	return s.q.SetPostNotified(ctx, db.SetPostNotifiedParams{
		ID: id, Revision: revision, TelegramMsgID: sql.NullInt64{Int64: messageID, Valid: messageID != 0},
	})
}

// PostWarnings returns the stored rule warnings of a post.
func (s *Service) PostWarnings(ctx context.Context, id int64) ([]string, error) {
	r, err := s.q.GetPost(ctx, id)
	if err != nil {
		return nil, wrapNotFound(err, fmt.Sprintf("post #%d", id))
	}
	return splitLines(r.Warnings), nil
}

func (s *Service) UnnotifiedEvents(ctx context.Context) ([]model.PostEvent, error) {
	rows, err := s.q.ListUnnotifiedEvents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.PostEvent, len(rows))
	for i, r := range rows {
		out[i] = eventModel(r)
	}
	return out, nil
}

func (s *Service) MarkEventNotified(ctx context.Context, id int64) error {
	return s.q.MarkEventNotified(ctx, id)
}

func addEvent(ctx context.Context, q *db.Queries, postID int64, action, actor, detail, now string) error {
	if actor == "" {
		actor = "unknown"
	}
	return q.AddPostEvent(ctx, db.AddPostEventParams{
		PostID: postID, Action: action, Actor: actor, Detail: detail,
		Notified: !notifyActions[action], Now: now,
	})
}

// Inbox ------------------------------------------------------------------------

func (s *Service) projectFor(ctx context.Context, q *db.Queries, projects []db.Project, m model.Mention) sql.NullInt64 {
	if m.Project != "" {
		for _, p := range projects {
			if p.Slug == m.Project {
				return sql.NullInt64{Int64: p.ID, Valid: true}
			}
		}
	}
	// A YouTube comment belongs to whichever project published that video.
	if m.Platform == model.PlatformYouTube && m.Context != "" {
		if post, err := q.FindPublishedByReleaseURL(ctx, db.FindPublishedByReleaseURLParams{
			Platform: model.PlatformYouTube, Needle: m.Context,
		}); err == nil {
			return sql.NullInt64{Int64: post.ProjectID, Valid: true}
		}
	}
	lower := strings.ToLower(m.Text + " " + m.Context)
	for _, p := range projects {
		if len(p.Name) >= 4 && strings.Contains(lower, strings.ToLower(p.Name)) {
			return sql.NullInt64{Int64: p.ID, Valid: true}
		}
	}
	return sql.NullInt64{}
}

func (s *Service) ImportMentions(ctx context.Context, items []model.Mention) (model.ImportResult, error) {
	res := model.ImportResult{Received: len(items)}
	err := s.inTx(ctx, func(q *db.Queries) error {
		projects, err := q.ListProjects(ctx, sql.NullString{})
		if err != nil {
			return err
		}
		now := db.FormatTime(s.now())
		for i, m := range items {
			if m.Platform == "" || m.ExternalID == "" {
				return invalid(fmt.Sprintf("item %d: platform and external_id are required", i))
			}
			if m.Kind == "" {
				m.Kind = "mention"
			}
			id, err := q.InsertMention(ctx, db.InsertMentionParams{
				Platform: m.Platform, Kind: m.Kind, ExternalID: m.ExternalID, Url: m.URL,
				Author: m.Author, Text: m.Text, Context: m.Context,
				ProjectID: s.projectFor(ctx, q, projects, m),
				PostedAt:  normalizeTime(m.PostedAt), Now: now,
			})
			if errors.Is(err, sql.ErrNoRows) {
				continue // already known
			}
			if err != nil {
				return err
			}
			res.Inserted++
			res.IDs = append(res.IDs, id)
		}
		return nil
	})
	return res, err
}

func (s *Service) ListMentions(ctx context.Context, handled *bool, platform string, limit int) ([]model.Mention, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListMentions(ctx, db.ListMentionsParams{
		Handled: nullBool(handled), Platform: nullString(platform), Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	return s.mentionModels(ctx, rows)
}

// ClaimNewMentions returns unhandled mentions nobody has been told about and
// marks them told, in one statement.
func (s *Service) ClaimNewMentions(ctx context.Context) ([]model.Mention, error) {
	rows, err := s.q.ClaimNewMentions(ctx, nullString(db.FormatTime(s.now())))
	if err != nil {
		return nil, err
	}
	return s.mentionModels(ctx, rows)
}

func (s *Service) SetMentionsHandled(ctx context.Context, ids []int64) (int64, error) {
	var total int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		for _, id := range ids {
			n, err := q.SetMentionHandled(ctx, db.SetMentionHandledParams{ID: id, Now: nullString(db.FormatTime(s.now()))})
			if err != nil {
				return err
			}
			total += n
		}
		return nil
	})
	return total, err
}

// RedactMentions blanks text and author of a platform's mentions first seen
// longer ago than olderThan.
func (s *Service) RedactMentions(ctx context.Context, platform string, olderThan time.Duration) (int64, error) {
	return s.q.RedactMentions(ctx, db.RedactMentionsParams{
		Platform: platform, Before: db.FormatTime(s.now().Add(-olderThan)),
	})
}

func (s *Service) mentionModels(ctx context.Context, rows []db.Mention) ([]model.Mention, error) {
	slugs, err := s.slugs(ctx, s.q)
	if err != nil {
		return nil, err
	}
	out := make([]model.Mention, len(rows))
	for i, r := range rows {
		out[i] = model.Mention{
			ID: r.ID, Platform: r.Platform, Kind: r.Kind, ExternalID: r.ExternalID, URL: r.Url,
			Author: r.Author, Text: r.Text, Context: r.Context, Project: slugs[r.ProjectID.Int64],
			PostedAt: r.PostedAt.String, SeenAt: r.SeenAt, Handled: r.Handled,
		}
	}
	return out, nil
}

func (s *Service) ImportReviews(ctx context.Context, items []model.Review) (model.ImportResult, error) {
	res := model.ImportResult{Received: len(items)}
	err := s.inTx(ctx, func(q *db.Queries) error {
		now := db.FormatTime(s.now())
		for i, r := range items {
			if r.Store != model.StoreAppStore && r.Store != model.StoreGooglePlay {
				return invalid(fmt.Sprintf("item %d: store must be appstore or googleplay", i))
			}
			if r.ExternalID == "" || r.AppID == "" {
				return invalid(fmt.Sprintf("item %d: app_id and external_id are required", i))
			}
			var projectID sql.NullInt64
			if r.Project != "" {
				if p, err := q.GetProjectBySlug(ctx, r.Project); err == nil {
					projectID = sql.NullInt64{Int64: p.ID, Valid: true}
				}
			}
			if !projectID.Valid {
				if p, err := q.FindProjectByAppID(ctx, r.AppID); err == nil {
					projectID = sql.NullInt64{Int64: p.ID, Valid: true}
				}
			}
			id, err := q.InsertReview(ctx, db.InsertReviewParams{
				Store: r.Store, AppID: r.AppID, ProjectID: projectID, ExternalID: r.ExternalID,
				Rating: int64(r.Rating), Title: r.Title, Text: r.Text, Author: r.Author,
				Version: r.Version, Territory: r.Territory, PostedAt: normalizeTime(r.PostedAt), Now: now,
			})
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			res.Inserted++
			res.IDs = append(res.IDs, id)
		}
		return nil
	})
	return res, err
}

func (s *Service) ListReviews(ctx context.Context, handled *bool, limit int) ([]model.Review, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListReviews(ctx, db.ListReviewsParams{Handled: nullBool(handled), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return s.reviewModels(ctx, rows)
}

func (s *Service) ClaimNewReviews(ctx context.Context) ([]model.Review, error) {
	rows, err := s.q.ClaimNewReviews(ctx, nullString(db.FormatTime(s.now())))
	if err != nil {
		return nil, err
	}
	return s.reviewModels(ctx, rows)
}

func (s *Service) SetReviewsHandled(ctx context.Context, ids []int64) (int64, error) {
	var total int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		for _, id := range ids {
			n, err := q.SetReviewHandled(ctx, db.SetReviewHandledParams{ID: id, Now: nullString(db.FormatTime(s.now()))})
			if err != nil {
				return err
			}
			total += n
		}
		return nil
	})
	return total, err
}

func (s *Service) reviewModels(ctx context.Context, rows []db.Review) ([]model.Review, error) {
	slugs, err := s.slugs(ctx, s.q)
	if err != nil {
		return nil, err
	}
	out := make([]model.Review, len(rows))
	for i, r := range rows {
		out[i] = model.Review{
			ID: r.ID, Store: r.Store, AppID: r.AppID, Project: slugs[r.ProjectID.Int64],
			ExternalID: r.ExternalID, Rating: int(r.Rating), Title: r.Title, Text: r.Text,
			Author: r.Author, Version: r.Version, Territory: r.Territory,
			PostedAt: r.PostedAt.String, SeenAt: r.SeenAt, Handled: r.Handled,
		}
	}
	return out, nil
}

// Digest summarises what happened since the given time.
func (s *Service) Digest(ctx context.Context, since time.Time) (model.Digest, error) {
	d := model.Digest{Since: db.FormatTime(since)}
	mentions, err := s.q.MentionsSeenSince(ctx, d.Since)
	if err != nil {
		return d, err
	}
	if d.Mentions, err = s.mentionModels(ctx, mentions); err != nil {
		return d, err
	}
	reviews, err := s.q.ReviewsSeenSince(ctx, d.Since)
	if err != nil {
		return d, err
	}
	if d.Reviews, err = s.reviewModels(ctx, reviews); err != nil {
		return d, err
	}
	if d.Drafts, err = s.ListPosts(ctx, PostFilter{Status: model.StatusDraft}); err != nil {
		return d, err
	}
	if d.Scheduled, err = s.ListPosts(ctx, PostFilter{Status: model.StatusScheduled}); err != nil {
		return d, err
	}
	if d.Failed, err = s.ListPosts(ctx, PostFilter{Status: model.StatusFailed}); err != nil {
		return d, err
	}
	log, err := s.PostLog(ctx, since)
	if err != nil {
		return d, err
	}
	d.Published = []model.Post{}
	for _, p := range log {
		if p.Status == model.StatusPublished && p.PublishedAt >= d.Since {
			d.Published = append(d.Published, p)
		}
	}
	return d, nil
}

// Conversions ------------------------------------------------------------------

func projectModel(r db.Project) model.Project {
	return model.Project{
		Slug: r.Slug, Name: r.Name, Tagline: r.Tagline, Audience: r.Audience,
		Tags: splitComma(r.Tags), Hooks: splitLines(r.Hooks), ForbiddenClaims: splitLines(r.ForbiddenClaims),
		WebsiteURL: r.WebsiteUrl, StoreIOSURL: r.StoreIosUrl, StoreAndroidURL: r.StoreAndroidUrl,
		IOSAppID: r.IosAppID, AndroidPackage: r.AndroidPackage, AssetsDir: r.AssetsDir,
		PostizAccounts: parseAccounts(r.PostizAccounts), DailyCap: int(r.DailyCap),
		MinDaysBetween: int(r.MinDaysBetween), Status: r.Status, UpdatedAt: r.UpdatedAt,
	}
}

func postModel(r db.Post, slug string) model.Post {
	return model.Post{
		ID: r.ID, Project: slug, Platform: r.Platform, Kind: r.Kind, Title: r.Title, Text: r.Text,
		MediaPaths: splitLines(r.MediaPaths), Account: r.Account, ReplyToURL: r.ReplyToUrl,
		Status: r.Status, Revision: r.Revision,
		CreatedBy: r.CreatedBy, PostizPostID: r.PostizPostID, ReleaseURL: r.ReleaseUrl, Error: r.Error,
		ScheduledAt: r.ScheduledAt.String, PublishedAt: r.PublishedAt.String,
		ApprovedAt: r.ApprovedAt.String, ApprovedBy: r.ApprovedBy, CreatedAt: r.CreatedAt,
		NotifiedRevision: r.NotifiedRevision, TelegramMsgID: r.TelegramMsgID.Int64,
	}
}

func eventModel(r db.PostEvent) model.PostEvent {
	return model.PostEvent{ID: r.ID, PostID: r.PostID, Action: r.Action, Actor: r.Actor, Detail: r.Detail, CreatedAt: r.CreatedAt}
}

// Accounts are stored as "platform=channel" lines so the column stays
// readable in sqlite3 and diffable in projects.yaml.
func parseAccounts(v string) map[string]string {
	out := map[string]string{}
	for _, line := range splitLines(v) {
		if k, val, ok := strings.Cut(line, "="); ok {
			if k, val = strings.TrimSpace(k), strings.TrimSpace(val); k != "" && val != "" {
				out[k] = val
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func formatAccounts(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			lines = append(lines, k+"="+v)
		}
	}
	return strings.Join(lines, "\n")
}

func accountFor(p db.Project, platform string) string {
	return parseAccounts(p.PostizAccounts)[platform]
}

func limitsOf(p db.Project) Limits {
	lim := Limits{PerDay: int(p.DailyCap), Gap: time.Duration(p.MinDaysBetween) * 24 * time.Hour}
	if lim.PerDay < 1 {
		lim.PerDay = DefaultLimits.PerDay
	}
	return lim
}

func nullString(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

func nullBool(v *bool) sql.NullBool {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *v, Valid: true}
}

// normalizeTime stores platform timestamps in the canonical format when they
// parse, and verbatim otherwise; losing one to a format quirk helps no one.
func normalizeTime(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return nullString(db.FormatTime(t))
	}
	return nullString(v)
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func splitComma(v string) []string { return trimAll(strings.Split(v, ",")) }

func splitLines(v string) []string { return trimAll(strings.Split(v, "\n")) }

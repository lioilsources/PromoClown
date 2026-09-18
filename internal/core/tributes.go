package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
)

// The tribute queue. The owner picks a picture somebody else made and sends it
// to the approval bot with the author's handle; the night job restyles it and
// drafts the post. Nothing here publishes anything — the draft goes through
// the same approval as every other post.

var creditRe = regexp.MustCompile(`^@[A-Za-z0-9_.-]{1,64}$`)

// NormalizeCredit accepts "@name", "name" or a profile URL and returns "@name".
func NormalizeCredit(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, "/"); i >= 0 {
		v = v[i+1:] // https://x.com/name → name
	}
	v = strings.TrimSpace(strings.TrimPrefix(v, "@"))
	if v == "" {
		return ""
	}
	return "@" + v
}

func (s *Service) EnqueueTribute(ctx context.Context, req model.TributeRequest) (model.Tribute, error) {
	credit := NormalizeCredit(req.Credit)
	source := strings.TrimSpace(req.SourcePath)

	var problems []string
	if !creditRe.MatchString(credit) {
		problems = append(problems, fmt.Sprintf("credit %q is not a handle like @artist", req.Credit))
	}
	if source == "" {
		problems = append(problems, "source_path is required")
	} else if problem := s.checkMedia(model.PlatformX, source); problem != "" {
		problems = append(problems, problem)
	} else if MediaKind(source) != "image" {
		problems = append(problems, "a tribute starts from an image")
	}
	if len(problems) > 0 {
		return model.Tribute{}, invalid(problems...)
	}

	var out model.Tribute
	err := s.inTx(ctx, func(q *db.Queries) error {
		proj, err := q.GetProjectBySlug(ctx, req.Project)
		if err != nil {
			return wrapNotFound(err, "project "+req.Project)
		}
		if proj.Status != "active" {
			return invalid(fmt.Sprintf("project %s is %s", proj.Slug, proj.Status))
		}
		row, err := q.EnqueueTribute(ctx, db.EnqueueTributeParams{
			ProjectID: proj.ID, Credit: credit, SourcePath: source,
			Note: strings.TrimSpace(req.Note), SubmittedBy: req.Actor,
			Now: db.FormatTime(s.now()),
		})
		if err != nil {
			return err
		}
		out = tributeModel(row, proj.Slug)
		return nil
	})
	return out, err
}

func (s *Service) ListTributes(ctx context.Context, status string, limit int) ([]model.Tribute, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListTributes(ctx, db.ListTributesParams{Status: nullString(status), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return s.tributeModels(ctx, rows)
}

func (s *Service) GetTribute(ctx context.Context, id int64) (model.Tribute, error) {
	r, err := s.q.GetTribute(ctx, id)
	if err != nil {
		return model.Tribute{}, wrapNotFound(err, fmt.Sprintf("tribute #%d", id))
	}
	out, err := s.tributeModels(ctx, []db.Tribute{r})
	if err != nil {
		return model.Tribute{}, err
	}
	return out[0], nil
}

// ClaimTribute takes the oldest queued tribute and marks it running. It
// returns ErrNotFound when the queue is empty, which is the normal end of a
// night's work rather than a failure.
func (s *Service) ClaimTribute(ctx context.Context) (model.Tribute, error) {
	var out model.Tribute
	err := s.inTx(ctx, func(q *db.Queries) error {
		row, err := q.ClaimTribute(ctx, db.FormatTime(s.now()))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no queued tribute: %w", ErrNotFound)
		}
		if err != nil {
			return err
		}
		proj, err := q.GetProjectByID(ctx, row.ProjectID)
		if err != nil {
			return err
		}
		out = tributeModel(row, proj.Slug)
		return nil
	})
	return out, err
}

// FinishTribute records what became of a claimed tribute: the draft it turned
// into, or why it did not.
func (s *Service) FinishTribute(ctx context.Context, id int64, req model.TributeDoneRequest) (model.Tribute, error) {
	var out model.Tribute
	err := s.inTx(ctx, func(q *db.Queries) error {
		now := db.FormatTime(s.now())
		var row db.Tribute
		var err error
		if msg := strings.TrimSpace(req.Error); msg != "" {
			row, err = q.TributeFailed(ctx, db.TributeFailedParams{ID: id, Error: msg, Now: now})
		} else if req.PostID == 0 {
			return invalid("post_id is required unless the tribute failed")
		} else {
			if _, err = q.GetPost(ctx, req.PostID); err != nil {
				return wrapNotFound(err, fmt.Sprintf("post #%d", req.PostID))
			}
			row, err = q.TributeDrafted(ctx, db.TributeDraftedParams{
				ID: id, PostID: sql.NullInt64{Int64: req.PostID, Valid: true},
				Styles: strings.Join(trimAll(req.Styles), "\n"), Now: now,
			})
		}
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tribute #%d is not in flight: %w", id, ErrConflict)
		}
		if err != nil {
			return err
		}
		proj, err := q.GetProjectByID(ctx, row.ProjectID)
		if err != nil {
			return err
		}
		out = tributeModel(row, proj.Slug)
		return nil
	})
	return out, err
}

// RetryTribute puts a failed or cancelled tribute back in the queue.
func (s *Service) RetryTribute(ctx context.Context, id int64) (model.Tribute, error) {
	return s.moveTribute(ctx, id, func(ctx context.Context, q *db.Queries, now string) (db.Tribute, error) {
		return q.RequeueTribute(ctx, db.RequeueTributeParams{ID: id, Now: now})
	}, "queued or failed")
}

func (s *Service) CancelTribute(ctx context.Context, id int64) (model.Tribute, error) {
	return s.moveTribute(ctx, id, func(ctx context.Context, q *db.Queries, now string) (db.Tribute, error) {
		return q.CancelTribute(ctx, db.CancelTributeParams{ID: id, Now: now})
	}, "waiting or failed")
}

func (s *Service) moveTribute(ctx context.Context, id int64,
	fn func(context.Context, *db.Queries, string) (db.Tribute, error), want string) (model.Tribute, error) {
	var out model.Tribute
	err := s.inTx(ctx, func(q *db.Queries) error {
		row, err := fn(ctx, q, db.FormatTime(s.now()))
		if errors.Is(err, sql.ErrNoRows) {
			if t, gerr := q.GetTribute(ctx, id); gerr == nil {
				return fmt.Errorf("tribute #%d is %s, not %s: %w", id, t.Status, want, ErrConflict)
			}
			return fmt.Errorf("tribute #%d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		proj, err := q.GetProjectByID(ctx, row.ProjectID)
		if err != nil {
			return err
		}
		out = tributeModel(row, proj.Slug)
		return nil
	})
	return out, err
}

// QueuedTributes is how many pictures are waiting for the next night's run.
func (s *Service) QueuedTributes(ctx context.Context) (int64, error) {
	return s.q.CountQueuedTributes(ctx)
}

func (s *Service) tributeModels(ctx context.Context, rows []db.Tribute) ([]model.Tribute, error) {
	slugs, err := s.slugs(ctx, s.q)
	if err != nil {
		return nil, err
	}
	out := make([]model.Tribute, len(rows))
	for i, r := range rows {
		out[i] = tributeModel(r, slugs[r.ProjectID])
	}
	return out, nil
}

func tributeModel(r db.Tribute, slug string) model.Tribute {
	return model.Tribute{
		ID: r.ID, Project: slug, Credit: r.Credit, SourcePath: r.SourcePath, Note: r.Note,
		Status: r.Status, PostID: r.PostID.Int64, Styles: splitLines(r.Styles), Error: r.Error,
		SubmittedBy: r.SubmittedBy, CreatedAt: r.CreatedAt,
	}
}

// Package api is promo-api's HTTP surface.
//
// Two tokens, two roles. The agent token (on the Spark, next to the LLM) can
// read everything, create drafts and feed the inbox. The admin token (only on
// JODA, inside the approval bot and publisher) can additionally edit, approve,
// reject and record publication. The agent therefore cannot publish even if
// it ignores every instruction it was given.
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/model"
)

const (
	TokenHeader = "X-Promo-Token"
	ActorHeader = "X-Promo-Actor"

	maxJSONBytes = 5 << 20
)

type Config struct {
	AgentToken     string
	AdminToken     string
	WebhookSecret  string
	MaxUploadBytes int64
}

// WebhookFunc receives a Postiz webhook body after the request was accepted.
type WebhookFunc func(ctx context.Context, body []byte)

type role int

const (
	roleNone role = iota
	roleAgent
	roleAdmin
)

type handler func(w http.ResponseWriter, r *http.Request, actor string)

type server struct {
	svc     *core.Service
	cfg     Config
	webhook WebhookFunc
	log     *slog.Logger
}

func New(svc *core.Service, cfg Config, webhook WebhookFunc, log *slog.Logger) http.Handler {
	if cfg.MaxUploadBytes == 0 {
		cfg.MaxUploadBytes = 512 << 20
	}
	s := &server{svc: svc, cfg: cfg, webhook: webhook, log: log}
	mux := http.NewServeMux()
	agent := func(pattern string, h handler) { mux.Handle(pattern, s.auth(roleAgent, h)) }
	admin := func(pattern string, h handler) { mux.Handle(pattern, s.auth(roleAdmin, h)) }

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "promo-api"})
	})

	agent("GET /projects", s.listProjects)
	agent("GET /projects/{slug}", s.getProject)
	agent("GET /projects/{slug}/assets", s.listAssets)
	agent("POST /projects/{slug}/assets", s.uploadAsset)
	agent("GET /assets/{path...}", s.getAsset)
	admin("POST /projects", s.upsertProjects)

	agent("GET /posts", s.listPosts)
	agent("POST /posts", s.createPost)
	agent("GET /posts/log", s.postLog)
	agent("GET /posts/{id}", s.getPost)
	agent("GET /posts/{id}/events", s.postEvents)
	admin("PATCH /posts/{id}", s.editPost)
	admin("POST /posts/{id}/approve", s.approvePost)
	admin("POST /posts/{id}/reject", s.rejectPost)
	admin("POST /posts/{id}/retry", s.retryPost)
	admin("POST /posts/{id}/published", s.markPublished)

	agent("GET /tributes", s.listTributes)
	agent("GET /tributes/{id}", s.getTribute)
	agent("POST /tributes/claim", s.claimTribute)
	agent("POST /tributes/{id}/done", s.finishTribute)
	admin("POST /tributes", s.enqueueTribute)
	admin("POST /tributes/{id}/retry", s.retryTribute)
	admin("POST /tributes/{id}/cancel", s.cancelTribute)

	agent("POST /mentions", s.importMentions)
	agent("GET /mentions", s.listMentions)
	agent("POST /mentions/claim", s.claimMentions)
	agent("POST /mentions/handled", s.handledMentions)

	agent("POST /reviews", s.importReviews)
	agent("GET /reviews", s.listReviews)
	agent("POST /reviews/claim", s.claimReviews)
	agent("POST /reviews/handled", s.handledReviews)

	agent("GET /digest", s.digest)

	mux.HandleFunc("POST /webhooks/postiz", s.postizWebhook)

	return s.logRequests(mux)
}

// Auth ----------------------------------------------------------------------------

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (s *server) roleOf(r *http.Request) role {
	tok := r.Header.Get(TokenHeader)
	switch {
	case tok == "":
		return roleNone
	case s.cfg.AdminToken != "" && equal(tok, s.cfg.AdminToken):
		return roleAdmin
	case s.cfg.AgentToken != "" && equal(tok, s.cfg.AgentToken):
		return roleAgent
	}
	return roleNone
}

func (s *server) auth(need role, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := s.roleOf(r)
		if got == roleNone {
			writeError(w, http.StatusUnauthorized, "missing or invalid "+TokenHeader)
			return
		}
		if got < need {
			writeError(w, http.StatusForbidden,
				"the agent token cannot approve, edit or publish; drafts are approved by a human in the Telegram approval bot")
			return
		}
		// The agent never gets to name itself something else.
		actor := core.ActorAgent
		if got == roleAdmin {
			actor = strings.TrimSpace(r.Header.Get(ActorHeader))
			if actor == "" || actor == core.ActorAgent {
				actor = "admin"
			}
		}
		h(w, r, actor)
	})
}

// Projects ------------------------------------------------------------------------

func (s *server) listProjects(w http.ResponseWriter, r *http.Request, _ string) {
	out, err := s.svc.ListProjects(r.Context(), r.URL.Query().Get("status"))
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request, _ string) {
	out, err := s.svc.GetProject(r.Context(), r.PathValue("slug"))
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) upsertProjects(w http.ResponseWriter, r *http.Request, _ string) {
	var list []model.Project
	if err := decodeOneOrMany(w, r, &list); err != nil {
		s.fail(w, err)
		return
	}
	out := make([]model.Project, 0, len(list))
	for _, p := range list {
		saved, err := s.svc.UpsertProject(r.Context(), p)
		if err != nil {
			s.fail(w, fmt.Errorf("project %q: %w", p.Slug, err))
			return
		}
		out = append(out, saved)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listAssets(w http.ResponseWriter, r *http.Request, _ string) {
	out, err := s.svc.ListAssets(r.Context(), r.PathValue("slug"))
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) uploadAsset(w http.ResponseWriter, r *http.Request, _ string) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	file, header, err := r.FormFile("file")
	if err != nil {
		s.fail(w, badRequest("multipart field \"file\" is required: %v", err))
		return
	}
	defer file.Close()
	out, err := s.svc.SaveUpload(r.Context(), r.PathValue("slug"), header.Filename, file)
	s.respond(w, http.StatusCreated, out, err)
}

// getAsset streams a stored asset back. The night job runs on the Spark while
// the assets live with promo-api, so it has to fetch the source picture before
// it can restyle it.
func (s *server) getAsset(w http.ResponseWriter, r *http.Request, _ string) {
	path, err := s.svc.AssetPath(r.PathValue("path"))
	if err != nil {
		s.fail(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.fail(w, fmt.Errorf("asset %s: %w", r.PathValue("path"), core.ErrNotFound))
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		s.fail(w, fmt.Errorf("asset %s: %w", r.PathValue("path"), core.ErrNotFound))
		return
	}
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

// Posts ---------------------------------------------------------------------------

func (s *server) listPosts(w http.ResponseWriter, r *http.Request, _ string) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := s.svc.ListPosts(r.Context(), core.PostFilter{
		Status: q.Get("status"), Project: q.Get("project"), Platform: q.Get("platform"), Limit: limit,
	})
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) createPost(w http.ResponseWriter, r *http.Request, actor string) {
	var req model.DraftRequest
	if err := decode(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.CreateDraft(r.Context(), req, actor)
	s.respond(w, http.StatusCreated, out, err)
}

func (s *server) postLog(w http.ResponseWriter, r *http.Request, _ string) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days <= 0 {
		days = 30
	}
	out, err := s.svc.PostLog(r.Context(), time.Now().Add(-time.Duration(days)*24*time.Hour))
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) getPost(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.GetPost(r.Context(), id)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) postEvents(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.PostEvents(r.Context(), id)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) editPost(w http.ResponseWriter, r *http.Request, actor string) {
	id, err := pathID(r)
	var req model.EditRequest
	if err == nil {
		err = decode(w, r, &req)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	req.Actor = actor
	out, err := s.svc.EditDraft(r.Context(), id, req)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) approvePost(w http.ResponseWriter, r *http.Request, actor string) {
	id, err := pathID(r)
	var req model.ApproveRequest
	if err == nil {
		err = decode(w, r, &req)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	req.Actor = actor
	out, err := s.svc.Approve(r.Context(), id, req)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) rejectPost(w http.ResponseWriter, r *http.Request, actor string) {
	id, err := pathID(r)
	var req model.RejectRequest
	if err == nil {
		err = decodeOptional(w, r, &req)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	req.Actor = actor
	out, err := s.svc.Reject(r.Context(), id, req)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) retryPost(w http.ResponseWriter, r *http.Request, actor string) {
	id, err := pathID(r)
	var req model.RetryRequest
	if err == nil {
		err = decodeOptional(w, r, &req)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	req.Actor = actor
	out, err := s.svc.Retry(r.Context(), id, req)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) markPublished(w http.ResponseWriter, r *http.Request, actor string) {
	id, err := pathID(r)
	var req model.PublishedRequest
	if err == nil {
		err = decodeOptional(w, r, &req)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.MarkPublished(r.Context(), id, req.URL, time.Time{}, actor)
	s.respond(w, http.StatusOK, out, err)
}

// Tributes -------------------------------------------------------------------------

func (s *server) listTributes(w http.ResponseWriter, r *http.Request, _ string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.svc.ListTributes(r.Context(), r.URL.Query().Get("status"), limit)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) getTribute(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.GetTribute(r.Context(), id)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) enqueueTribute(w http.ResponseWriter, r *http.Request, actor string) {
	var req model.TributeRequest
	if err := decode(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	req.Actor = actor
	out, err := s.svc.EnqueueTribute(r.Context(), req)
	s.respond(w, http.StatusCreated, out, err)
}

// claimTribute hands the night job the next picture to work on. An empty queue
// answers 404, which is the worker's signal to stop.
func (s *server) claimTribute(w http.ResponseWriter, r *http.Request, _ string) {
	out, err := s.svc.ClaimTribute(r.Context())
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) finishTribute(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req model.TributeDoneRequest
	if err := decode(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.FinishTribute(r.Context(), id, req)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) retryTribute(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.RetryTribute(r.Context(), id)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) cancelTribute(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.CancelTribute(r.Context(), id)
	s.respond(w, http.StatusOK, out, err)
}

// Inbox ---------------------------------------------------------------------------

func handledParam(r *http.Request) (*bool, error) {
	v := r.URL.Query().Get("handled")
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, badRequest("handled must be true or false")
	}
	return &b, nil
}

func (s *server) importMentions(w http.ResponseWriter, r *http.Request, _ string) {
	var items []model.Mention
	if err := decodeOneOrMany(w, r, &items); err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.ImportMentions(r.Context(), items)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) listMentions(w http.ResponseWriter, r *http.Request, _ string) {
	handled, err := handledParam(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.svc.ListMentions(r.Context(), handled, r.URL.Query().Get("platform"), limit)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) claimMentions(w http.ResponseWriter, r *http.Request, _ string) {
	out, err := s.svc.ClaimNewMentions(r.Context())
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) handledMentions(w http.ResponseWriter, r *http.Request, _ string) {
	var req model.IDsRequest
	if err := decode(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.svc.SetMentionsHandled(r.Context(), req.IDs)
	s.respond(w, http.StatusOK, model.CountResult{Updated: n}, err)
}

func (s *server) importReviews(w http.ResponseWriter, r *http.Request, _ string) {
	var items []model.Review
	if err := decodeOneOrMany(w, r, &items); err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.ImportReviews(r.Context(), items)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) listReviews(w http.ResponseWriter, r *http.Request, _ string) {
	handled, err := handledParam(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.svc.ListReviews(r.Context(), handled, limit)
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) claimReviews(w http.ResponseWriter, r *http.Request, _ string) {
	out, err := s.svc.ClaimNewReviews(r.Context())
	s.respond(w, http.StatusOK, out, err)
}

func (s *server) handledReviews(w http.ResponseWriter, r *http.Request, _ string) {
	var req model.IDsRequest
	if err := decode(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.svc.SetReviewsHandled(r.Context(), req.IDs)
	s.respond(w, http.StatusOK, model.CountResult{Updated: n}, err)
}

// ParseSince reads "24h"/"7d" style durations or an RFC 3339 time.
func ParseSince(v string, now time.Time) (time.Time, error) {
	if v == "" {
		return now.Add(-24 * time.Hour), nil
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, badRequest("since must be a duration like 24h or 7d, or an RFC 3339 time")
}

func (s *server) digest(w http.ResponseWriter, r *http.Request, _ string) {
	since, err := ParseSince(r.URL.Query().Get("since"), time.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.svc.Digest(r.Context(), since)
	s.respond(w, http.StatusOK, out, err)
}

// Postiz webhook -------------------------------------------------------------------

// Postiz cannot sign webhooks or send headers, so the secret rides in the URL.
// The body is only a hint: the handler re-reads the post state from Postiz.
func (s *server) postizWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WebhookSecret == "" || !equal(r.URL.Query().Get("secret"), s.cfg.WebhookSecret) {
		writeError(w, http.StatusUnauthorized, "bad webhook secret")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	if s.webhook != nil {
		go s.webhook(context.WithoutCancel(r.Context()), body)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// Plumbing --------------------------------------------------------------------------

type requestError struct {
	status int
	msg    string
}

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.PathValue("id"), "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("post id must be a positive number")
	}
	return id, nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

// decodeOptional accepts an empty body as the zero value.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	if err != nil {
		return badRequest("cannot read body: %v", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

// decodeOneOrMany fills a slice from either a JSON array or a single object.
func decodeOneOrMany[T any](w http.ResponseWriter, r *http.Request, out *[]T) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	if err != nil {
		return badRequest("cannot read body: %v", err)
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] != '[' {
		body = append(append([]byte{'['}, body...), ']')
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

func (s *server) respond(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *server) fail(w http.ResponseWriter, err error) {
	var verr *core.ValidationError
	var rerr *requestError
	var mberr *http.MaxBytesError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusUnprocessableEntity, model.Error{Error: err.Error(), Problems: verr.Problems})
	case errors.As(err, &rerr):
		writeError(w, rerr.status, rerr.msg)
	case errors.As(err, &mberr):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, core.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, core.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, model.Error{Error: msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/health" {
			return
		}
		// Never log the query string: the webhook secret lives there.
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds())
	})
}

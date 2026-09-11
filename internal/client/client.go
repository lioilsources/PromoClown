// Package client talks to promo-api over HTTP. The promo CLI uses it on the
// Spark with the agent token, and on the Mac with the admin token.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
)

type Client struct {
	BaseURL string
	Token   string
	// Actor names a human caller in the audit trail; promo-api ignores it for
	// the agent token.
	Actor string
	HTTP  *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// APIError is a non-2xx answer from promo-api.
type APIError struct {
	Status   int
	Message  string
	Problems []string
}

func (e *APIError) Error() string {
	if len(e.Problems) > 1 {
		return fmt.Sprintf("promo-api %d: %s", e.Status, "\n  - "+strings.Join(e.Problems, "\n  - "))
	}
	return fmt.Sprintf("promo-api %d: %s", e.Status, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(req, out)
}

func (c *Client) send(req *http.Request, out any) error {
	req.Header.Set("X-Promo-Token", c.Token)
	if c.Actor != "" {
		req.Header.Set("X-Promo-Actor", c.Actor)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e model.Error
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Message: e.Error, Problems: e.Problems}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Projects -----------------------------------------------------------------------

func (c *Client) Projects(ctx context.Context, status string) ([]model.Project, error) {
	var out []model.Project
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	return out, c.do(ctx, http.MethodGet, "/projects", q, nil, &out)
}

func (c *Client) Project(ctx context.Context, slug string) (model.Project, error) {
	var out model.Project
	return out, c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(slug), nil, nil, &out)
}

func (c *Client) UpsertProjects(ctx context.Context, projects []model.Project) ([]model.Project, error) {
	var out []model.Project
	return out, c.do(ctx, http.MethodPost, "/projects", nil, projects, &out)
}

func (c *Client) Assets(ctx context.Context, slug string) ([]model.Asset, error) {
	var out []model.Asset
	return out, c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(slug)+"/assets", nil, nil, &out)
}

// UploadAsset streams a local file to promo-api and returns its media path.
func (c *Client) UploadAsset(ctx context.Context, slug, path string) (model.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.Asset{}, err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(path))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/projects/"+url.PathEscape(slug)+"/assets", pr)
	if err != nil {
		return model.Asset{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out model.Asset
	return out, c.send(req, &out)
}

// Posts ----------------------------------------------------------------------------

type PostQuery struct {
	Status, Project, Platform string
	Limit                     int
}

func (c *Client) Posts(ctx context.Context, pq PostQuery) ([]model.Post, error) {
	q := url.Values{}
	for k, v := range map[string]string{"status": pq.Status, "project": pq.Project, "platform": pq.Platform} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if pq.Limit > 0 {
		q.Set("limit", strconv.Itoa(pq.Limit))
	}
	var out []model.Post
	return out, c.do(ctx, http.MethodGet, "/posts", q, nil, &out)
}

func (c *Client) Post(ctx context.Context, id int64) (model.Post, error) {
	var out model.Post
	return out, c.do(ctx, http.MethodGet, fmt.Sprintf("/posts/%d", id), nil, nil, &out)
}

func (c *Client) PostEvents(ctx context.Context, id int64) ([]model.PostEvent, error) {
	var out []model.PostEvent
	return out, c.do(ctx, http.MethodGet, fmt.Sprintf("/posts/%d/events", id), nil, nil, &out)
}

func (c *Client) PostLog(ctx context.Context, days int) ([]model.Post, error) {
	var out []model.Post
	return out, c.do(ctx, http.MethodGet, "/posts/log", url.Values{"days": {strconv.Itoa(days)}}, nil, &out)
}

func (c *Client) Draft(ctx context.Context, req model.DraftRequest) (model.PostResult, error) {
	var out model.PostResult
	return out, c.do(ctx, http.MethodPost, "/posts", nil, req, &out)
}

func (c *Client) Edit(ctx context.Context, id int64, req model.EditRequest) (model.PostResult, error) {
	var out model.PostResult
	return out, c.do(ctx, http.MethodPatch, fmt.Sprintf("/posts/%d", id), nil, req, &out)
}

func (c *Client) Approve(ctx context.Context, id int64, req model.ApproveRequest) (model.PostResult, error) {
	var out model.PostResult
	return out, c.do(ctx, http.MethodPost, fmt.Sprintf("/posts/%d/approve", id), nil, req, &out)
}

func (c *Client) Reject(ctx context.Context, id int64, req model.RejectRequest) (model.Post, error) {
	var out model.Post
	return out, c.do(ctx, http.MethodPost, fmt.Sprintf("/posts/%d/reject", id), nil, req, &out)
}

func (c *Client) Retry(ctx context.Context, id int64, req model.RetryRequest) (model.PostResult, error) {
	var out model.PostResult
	return out, c.do(ctx, http.MethodPost, fmt.Sprintf("/posts/%d/retry", id), nil, req, &out)
}

func (c *Client) MarkPublished(ctx context.Context, id int64, req model.PublishedRequest) (model.Post, error) {
	var out model.Post
	return out, c.do(ctx, http.MethodPost, fmt.Sprintf("/posts/%d/published", id), nil, req, &out)
}

// Inbox ----------------------------------------------------------------------------

func inboxQuery(handled *bool, platform string, limit int) url.Values {
	q := url.Values{}
	if handled != nil {
		q.Set("handled", strconv.FormatBool(*handled))
	}
	if platform != "" {
		q.Set("platform", platform)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

func (c *Client) ImportMentions(ctx context.Context, items []model.Mention) (model.ImportResult, error) {
	var out model.ImportResult
	return out, c.do(ctx, http.MethodPost, "/mentions", nil, items, &out)
}

func (c *Client) Mentions(ctx context.Context, handled *bool, platform string, limit int) ([]model.Mention, error) {
	var out []model.Mention
	return out, c.do(ctx, http.MethodGet, "/mentions", inboxQuery(handled, platform, limit), nil, &out)
}

func (c *Client) ClaimMentions(ctx context.Context) ([]model.Mention, error) {
	var out []model.Mention
	return out, c.do(ctx, http.MethodPost, "/mentions/claim", nil, nil, &out)
}

func (c *Client) MentionsHandled(ctx context.Context, ids []int64) (model.CountResult, error) {
	var out model.CountResult
	return out, c.do(ctx, http.MethodPost, "/mentions/handled", nil, model.IDsRequest{IDs: ids}, &out)
}

func (c *Client) ImportReviews(ctx context.Context, items []model.Review) (model.ImportResult, error) {
	var out model.ImportResult
	return out, c.do(ctx, http.MethodPost, "/reviews", nil, items, &out)
}

func (c *Client) Reviews(ctx context.Context, handled *bool, limit int) ([]model.Review, error) {
	var out []model.Review
	return out, c.do(ctx, http.MethodGet, "/reviews", inboxQuery(handled, "", limit), nil, &out)
}

func (c *Client) ClaimReviews(ctx context.Context) ([]model.Review, error) {
	var out []model.Review
	return out, c.do(ctx, http.MethodPost, "/reviews/claim", nil, nil, &out)
}

func (c *Client) ReviewsHandled(ctx context.Context, ids []int64) (model.CountResult, error) {
	var out model.CountResult
	return out, c.do(ctx, http.MethodPost, "/reviews/handled", nil, model.IDsRequest{IDs: ids}, &out)
}

func (c *Client) Digest(ctx context.Context, since string) (model.Digest, error) {
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	var out model.Digest
	return out, c.do(ctx, http.MethodGet, "/digest", q, nil, &out)
}

// Package postiz is a client for the Postiz Public API (self-hosted,
// /api/public/v1). Only what the publisher needs: integrations, uploads,
// creating scheduled posts and reading their state back.
package postiz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	// BaseURL ends in /public/v1, e.g. http://postiz:5000/api/public/v1.
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		// Video uploads can be large; the per-call context bounds the rest.
		HTTP: &http.Client{Timeout: 30 * time.Minute},
	}
}

// Error is a non-2xx answer. Status 4xx other than 429 will not get better on
// retry.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("postiz %d: %s", e.Status, e.Body) }

// Permanent reports whether retrying the same request is pointless.
func (e *Error) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusUnauthorized
}

type Integration struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Picture    string `json:"picture"`
	Disabled   bool   `json:"disabled"`
	Profile    string `json:"profile"`
}

type Media struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Tag struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type Value struct {
	Content string  `json:"content"`
	Image   []Media `json:"image"`
}

type IntegrationRef struct {
	ID string `json:"id"`
}

type Entry struct {
	Integration IntegrationRef `json:"integration"`
	Value       []Value        `json:"value"`
	Settings    map[string]any `json:"settings"`
}

type CreateRequest struct {
	Type      string  `json:"type"` // draft | schedule | now
	Date      string  `json:"date"`
	ShortLink bool    `json:"shortLink"`
	Tags      []Tag   `json:"tags"`
	Posts     []Entry `json:"posts"`
}

type Created struct {
	PostID      string `json:"postId"`
	Integration string `json:"integration"`
}

const (
	StateQueue     = "QUEUE"
	StatePublished = "PUBLISHED"
	StateError     = "ERROR"
	StateDraft     = "DRAFT"
)

type Post struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	PublishDate string `json:"publishDate"`
	ReleaseURL  string `json:"releaseURL"`
	ReleaseID   string `json:"releaseId"`
	State       string `json:"state"`
	Group       string `json:"group"`
	Integration struct {
		ID                 string `json:"id"`
		ProviderIdentifier string `json:"providerIdentifier"`
		Name               string `json:"name"`
	} `json:"integration"`
}

// FormatDate renders a time the way Postiz writes them.
func FormatDate(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", c.APIKey) // raw key, no "Bearer"
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		body := strings.TrimSpace(string(data))
		if len(body) > 500 {
			body = body[:500] + "…"
		}
		return &Error{Status: resp.StatusCode, Body: body}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("postiz: decode %s: %w", req.URL.Path, err)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) IsConnected(ctx context.Context) (bool, error) {
	var out struct {
		Connected bool `json:"connected"`
	}
	err := c.getJSON(ctx, "/is-connected", nil, &out)
	return out.Connected, err
}

func (c *Client) Integrations(ctx context.Context) ([]Integration, error) {
	var out []Integration
	return out, c.getJSON(ctx, "/integrations", nil, &out)
}

// Upload streams a file as multipart field "file". Postiz checks the type by
// content: images up to 10 MB, mp4 up to 1 GB.
func (c *Client) Upload(ctx context.Context, filename string, r io.Reader) (Media, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filename)
		if err == nil {
			_, err = io.Copy(part, r)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/upload", pr)
	if err != nil {
		return Media{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out Media
	return out, c.do(req, &out)
}

func (c *Client) CreatePost(ctx context.Context, cr CreateRequest) ([]Created, error) {
	buf, err := json.Marshal(cr)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/posts", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var out []Created
	return out, c.do(req, &out)
}

// Posts lists top-level posts whose publish date falls in [start, end].
func (c *Client) Posts(ctx context.Context, start, end time.Time) ([]Post, error) {
	var out struct {
		Posts []Post `json:"posts"`
	}
	err := c.getJSON(ctx, "/posts", url.Values{"startDate": {FormatDate(start)}, "endDate": {FormatDate(end)}}, &out)
	return out.Posts, err
}

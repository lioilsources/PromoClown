// Package bluesky reads mentions, replies and quotes of the account from its
// notifications, logged in with an app password.
//
// createSession is limited to 30 calls per 5 minutes and 300 per day, and the
// monitor runs every few minutes, so the session is cached on disk and renewed
// with refreshSession; a fresh login is the last resort.
package bluesky

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const (
	DefaultService = "https://bsky.social"

	maxPages = 20
)

var wantedReasons = []string{"mention", "reply", "quote"}

// Session is what createSession and refreshSession return, plus the login
// identifier so a cache left by another account is never reused.
type Session struct {
	Identifier string          `json:"identifier"`
	DID        string          `json:"did"`
	Handle     string          `json:"handle"`
	AccessJwt  string          `json:"accessJwt"`
	RefreshJwt string          `json:"refreshJwt"`
	DIDDoc     json.RawMessage `json:"didDoc,omitempty"`
}

// PDS is the account's own server from the DID document, or "" if unknown.
func (s *Session) PDS() string {
	var doc struct {
		Service []struct {
			ID              string `json:"id"`
			Type            string `json:"type"`
			ServiceEndpoint any    `json:"serviceEndpoint"`
		} `json:"service"`
	}
	if len(s.DIDDoc) == 0 || json.Unmarshal(s.DIDDoc, &doc) != nil {
		return ""
	}
	for _, svc := range doc.Service {
		if strings.HasSuffix(svc.ID, "#atproto_pds") && svc.Type == "AtprotoPersonalDataServer" {
			if ep, ok := svc.ServiceEndpoint.(string); ok {
				return strings.TrimRight(ep, "/")
			}
		}
	}
	return ""
}

type Client struct {
	Service     string
	HTTP        *http.Client
	Identifier  string
	Password    string
	SessionFile string

	session *Session
}

func New(service, identifier, password, sessionFile string) *Client {
	if service == "" {
		service = DefaultService
	}
	return &Client{
		Service: strings.TrimRight(service, "/"), HTTP: cliutil.HTTPClient(),
		Identifier: identifier, Password: password, SessionFile: sessionFile,
	}
}

// DefaultSessionFile is $XDG_CACHE_HOME (or ~/.cache)/promoclown/bluesky-session.json.
func DefaultSessionFile() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "promoclown", "bluesky-session.json")
}

type xrpcError struct {
	Status  int    `json:"-"`
	Name    string `json:"error"`
	Message string `json:"message"`
}

func (e *xrpcError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("bluesky HTTP %d: %s %s", e.Status, e.Name, e.Message))
}

func (e *xrpcError) expired() bool {
	return e.Status == http.StatusUnauthorized ||
		(e.Status == http.StatusBadRequest && (e.Name == "ExpiredToken" || e.Name == "InvalidToken"))
}

func (c *Client) do(ctx context.Context, method, u, bearer string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xe := &xrpcError{Status: resp.StatusCode}
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(xe)
		return xe
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) loadSession() {
	if c.SessionFile == "" {
		return
	}
	b, err := os.ReadFile(c.SessionFile)
	if err != nil {
		return
	}
	var s Session
	if json.Unmarshal(b, &s) != nil || s.AccessJwt == "" || s.Identifier != c.Identifier {
		return
	}
	c.session = &s
}

func (c *Client) saveSession() error {
	if c.SessionFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.SessionFile), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c.session)
	if err != nil {
		return err
	}
	tmp := c.SessionFile + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.SessionFile)
}

func (c *Client) createSession(ctx context.Context) error {
	var s Session
	err := c.do(ctx, http.MethodPost, c.Service+"/xrpc/com.atproto.server.createSession", "",
		map[string]string{"identifier": c.Identifier, "password": c.Password}, &s)
	if err != nil {
		return fmt.Errorf("createSession: %w", err)
	}
	s.Identifier = c.Identifier
	c.session = &s
	return c.saveSession()
}

func (c *Client) refreshSession(ctx context.Context) error {
	base := c.session.PDS()
	if base == "" {
		base = c.Service
	}
	var s Session
	if err := c.do(ctx, http.MethodPost, base+"/xrpc/com.atproto.server.refreshSession", c.session.RefreshJwt, nil, &s); err != nil {
		return fmt.Errorf("refreshSession: %w", err)
	}
	if s.AccessJwt == "" {
		return errors.New("refreshSession: no accessJwt in response")
	}
	s.Identifier = c.Identifier
	if len(s.DIDDoc) == 0 {
		s.DIDDoc = c.session.DIDDoc
	}
	c.session = &s
	return c.saveSession()
}

// authed GETs an XRPC method on the PDS, renewing the session once if the
// access token has expired.
func (c *Client) authed(ctx context.Context, method string, q url.Values, out any) error {
	if c.session == nil {
		c.loadSession()
	}
	if c.session == nil {
		if err := c.createSession(ctx); err != nil {
			return err
		}
	}
	call := func() error {
		base := c.session.PDS()
		if base == "" {
			base = c.Service
		}
		return c.do(ctx, http.MethodGet, base+"/xrpc/"+method+"?"+q.Encode(), c.session.AccessJwt, nil, out)
	}
	err := call()
	var xe *xrpcError
	if !errors.As(err, &xe) || !xe.expired() {
		return err
	}
	if rerr := c.refreshSession(ctx); rerr != nil {
		if cerr := c.createSession(ctx); cerr != nil {
			return fmt.Errorf("session expired; %v; %w", rerr, cerr)
		}
	}
	return call()
}

type notificationsPage struct {
	Cursor        string `json:"cursor"`
	Notifications []struct {
		URI    string `json:"uri"`
		Author struct {
			DID    string `json:"did"`
			Handle string `json:"handle"`
		} `json:"author"`
		Reason        string `json:"reason"`
		ReasonSubject string `json:"reasonSubject"`
		Record        struct {
			Text      string `json:"text"`
			CreatedAt string `json:"createdAt"`
		} `json:"record"`
		IndexedAt string `json:"indexedAt"`
	} `json:"notifications"`
}

// PostURL turns at://<did>/app.bsky.feed.post/<rkey> into a bsky.app link.
// The DID form is kept because handles can change.
func PostURL(uri string) string {
	rest, ok := strings.CutPrefix(uri, "at://")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "app.bsky.feed.post" {
		return ""
	}
	return "https://bsky.app/profile/" + parts[0] + "/post/" + parts[2]
}

// Mentions returns mentions, replies and quotes posted at or after since.
func (c *Client) Mentions(ctx context.Context, since time.Time) ([]model.Mention, error) {
	var out []model.Mention
	cursor := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{"limit": {"100"}, "reasons": wantedReasons}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var body notificationsPage
		if err := c.authed(ctx, "app.bsky.notification.listNotifications", q, &body); err != nil {
			return out, err
		}
		older := false
		for _, n := range body.Notifications {
			indexed, _ := time.Parse(time.RFC3339, n.IndexedAt)
			if !indexed.IsZero() && indexed.Before(since) {
				older = true // newest first: the rest is older too
				break
			}
			// The reasons filter is a request, not a guarantee; check again.
			if n.Reason != "mention" && n.Reason != "reply" && n.Reason != "quote" {
				continue
			}
			posted, err := time.Parse(time.RFC3339, n.Record.CreatedAt)
			if err != nil {
				posted = indexed
			}
			if posted.Before(since) {
				continue
			}
			m := model.Mention{
				Platform:   model.PlatformBluesky,
				Kind:       n.Reason,
				ExternalID: n.URI,
				URL:        PostURL(n.URI),
				Author:     n.Author.Handle,
				Text:       n.Record.Text,
				Context:    n.ReasonSubject,
			}
			if !posted.IsZero() {
				m.PostedAt = cliutil.FormatTime(posted)
			}
			out = append(out, m)
		}
		if older || body.Cursor == "" || len(body.Notifications) == 0 {
			break
		}
		cursor = body.Cursor
	}
	return out, nil
}

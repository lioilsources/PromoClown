// Package reddit reads keyword hits and the inbox of a Reddit script app.
//
// Read-only by construction: the token asks for the read and privatemessages
// scopes only, and inbox reads pass mark=false so nothing is marked read.
// Reddit's Responsible Builder Policy requires approved API access, and asks
// that stored user content not be kept longer than needed.
package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const (
	DefaultAuthURL = "https://www.reddit.com"
	DefaultAPIURL  = "https://oauth.reddit.com"

	webURL       = "https://www.reddit.com"
	maxSelftext  = 2000
	maxRateSleep = 60 * time.Second
)

var subRe = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)

type Client struct {
	AuthURL      string
	APIURL       string
	HTTP         *http.Client
	ClientID     string
	ClientSecret string
	Username     string
	Password     string
	UserAgent    string
	Now          func() time.Time
	Sleep        func(time.Duration)

	token    string
	tokenExp time.Time
	wait     time.Duration
}

func New(clientID, clientSecret, username, password, userAgent string) *Client {
	return &Client{
		AuthURL: DefaultAuthURL, APIURL: DefaultAPIURL, HTTP: cliutil.HTTPClient(),
		ClientID: clientID, ClientSecret: clientSecret, Username: username, Password: password,
		UserAgent: userAgent, Now: time.Now, Sleep: time.Sleep,
	}
}

func (c *Client) authenticate(ctx context.Context) error {
	form := url.Values{
		"grant_type": {"password"},
		"username":   {c.Username},
		"password":   {c.Password},
		"scope":      {"read privatemessages"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AuthURL+"/api/v1/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cliutil.CheckResponse(resp); err != nil {
		return fmt.Errorf("reddit token: %w", err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("reddit token: %w", err)
	}
	// Reddit reports bad credentials as 200 {"error": "invalid_grant"}.
	if tok.AccessToken == "" {
		if tok.Error == "" {
			tok.Error = "no access_token in response"
		}
		return fmt.Errorf("reddit token: %s", tok.Error)
	}
	c.token = tok.AccessToken
	c.tokenExp = c.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, v any) error {
	for attempt := 0; ; attempt++ {
		if c.token == "" || !c.Now().Before(c.tokenExp.Add(-time.Minute)) {
			if err := c.authenticate(ctx); err != nil {
				return err
			}
		}
		if c.wait > 0 {
			c.Sleep(c.wait)
			c.wait = 0
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIURL+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "bearer "+c.token)
		req.Header.Set("User-Agent", c.UserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		c.noteRateLimit(resp.Header)
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.token = "" // revoked or expired early: one fresh token, then give up
			continue
		}
		err = cliutil.CheckResponse(resp)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(v)
		}
		resp.Body.Close()
		return err
	}
}

// noteRateLimit pauses before the next call when the budget is nearly spent;
// the headers are floats and the reset is in seconds.
func (c *Client) noteRateLimit(h http.Header) {
	remaining, err1 := strconv.ParseFloat(h.Get("X-Ratelimit-Remaining"), 64)
	reset, err2 := strconv.ParseFloat(h.Get("X-Ratelimit-Reset"), 64)
	if err1 != nil || err2 != nil || remaining >= 2 {
		return
	}
	d := time.Duration(reset * float64(time.Second))
	if d > maxRateSleep {
		d = maxRateSleep
	}
	c.wait = d
}

type listing struct {
	Data struct {
		Children []struct {
			Kind string `json:"kind"`
			Data struct {
				ID         string  `json:"id"`
				Name       string  `json:"name"`
				Subreddit  string  `json:"subreddit"`
				Title      string  `json:"title"`
				Selftext   string  `json:"selftext"`
				Author     string  `json:"author"`
				Permalink  string  `json:"permalink"`
				CreatedUTC float64 `json:"created_utc"`
				Body       string  `json:"body"`
				Context    string  `json:"context"`
				Subject    string  `json:"subject"`
				Type       string  `json:"type"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// TimeWindow is the coarsest search window t= that still covers since.
func TimeWindow(since, now time.Time) string {
	switch d := now.Sub(since); {
	case d <= 24*time.Hour:
		return "day"
	case d <= 7*24*time.Hour:
		return "week"
	case d <= 31*24*time.Hour:
		return "month"
	case d <= 366*24*time.Hour:
		return "year"
	}
	return "all"
}

func quoteKeyword(kw string) string {
	if strings.ContainsAny(kw, " \t") && !strings.HasPrefix(kw, `"`) {
		return `"` + kw + `"`
	}
	return kw
}

// Search finds posts matching any keyword in the given subreddits. Reddit
// search does not cover comments.
func (c *Client) Search(ctx context.Context, subs, keywords []string, since time.Time, limit int) ([]model.Mention, error) {
	if len(subs) == 0 || len(keywords) == 0 {
		return nil, errors.New("search needs at least one subreddit and one keyword")
	}
	clean := make([]string, 0, len(subs))
	for _, s := range subs {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "/"), "r/")
		if !subRe.MatchString(s) {
			return nil, fmt.Errorf("invalid subreddit name %q", s)
		}
		clean = append(clean, s)
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	path := "/r/" + strings.Join(clean, "+") + "/search"
	window := TimeWindow(since, c.Now())

	seen := map[string]bool{}
	var out []model.Mention
	for _, kw := range keywords {
		q := url.Values{
			"q":           {quoteKeyword(kw)},
			"restrict_sr": {"1"},
			"sort":        {"new"},
			"t":           {window},
			"type":        {"link"},
			"limit":       {strconv.Itoa(limit)},
			"raw_json":    {"1"}, // selftext without &amp; escaping
		}
		var l listing
		if err := c.get(ctx, path, q, &l); err != nil {
			return out, fmt.Errorf("search %q: %w", kw, err)
		}
		for _, child := range l.Data.Children {
			d := child.Data
			created := time.Unix(int64(d.CreatedUTC), 0)
			if seen[d.Name] || created.Before(since) {
				continue
			}
			seen[d.Name] = true
			text := d.Title
			if st := strings.TrimSpace(d.Selftext); st != "" {
				text += "\n\n" + cliutil.Truncate(st, maxSelftext)
			}
			out = append(out, model.Mention{
				Platform:   model.PlatformReddit,
				Kind:       "search",
				ExternalID: d.Name,
				URL:        webURL + d.Permalink,
				Author:     d.Author,
				Text:       text,
				Context:    d.Subreddit,
				PostedAt:   cliutil.FormatTime(created),
			})
		}
	}
	return out, nil
}

// Inbox returns comment replies, username mentions and private messages.
func (c *Client) Inbox(ctx context.Context, since time.Time, limit int) ([]model.Mention, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	q := url.Values{"mark": {"false"}, "limit": {strconv.Itoa(limit)}, "raw_json": {"1"}}
	var l listing
	if err := c.get(ctx, "/message/inbox", q, &l); err != nil {
		return nil, fmt.Errorf("inbox: %w", err)
	}
	var out []model.Mention
	for _, child := range l.Data.Children {
		d := child.Data
		created := time.Unix(int64(d.CreatedUTC), 0)
		if created.Before(since) {
			continue
		}
		kind := d.Type
		if kind == "" {
			kind = "message"
		}
		link := ""
		switch {
		case d.Context != "":
			link = webURL + d.Context
		case child.Kind == "t4" && d.ID != "":
			link = webURL + "/message/messages/" + d.ID
		}
		ctxValue := d.Subreddit
		if ctxValue == "" {
			ctxValue = d.Subject
		}
		out = append(out, model.Mention{
			Platform:   model.PlatformReddit,
			Kind:       kind,
			ExternalID: d.Name,
			URL:        link,
			Author:     d.Author,
			Text:       d.Body,
			Context:    ctxValue,
			PostedAt:   cliutil.FormatTime(created),
		})
	}
	return out, nil
}

// Package youtube reads comments on the channel's videos with the YouTube
// Data API. An API key is enough for public comments; held-for-review and
// spam comments would need OAuth as the channel owner and are not read.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const DefaultBaseURL = "https://www.googleapis.com/youtube/v3"

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	APIKey    string
	ChannelID string
}

func New(apiKey, channelID string) *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: cliutil.HTTPClient(), APIKey: apiKey, ChannelID: channelID}
}

type threadsPage struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		Snippet struct {
			VideoID         string `json:"videoId"`
			TopLevelComment struct {
				ID      string `json:"id"`
				Snippet struct {
					AuthorDisplayName string `json:"authorDisplayName"`
					AuthorChannelID   struct {
						Value string `json:"value"`
					} `json:"authorChannelId"`
					TextDisplay string `json:"textDisplay"`
					VideoID     string `json:"videoId"`
					PublishedAt string `json:"publishedAt"`
				} `json:"snippet"`
			} `json:"topLevelComment"`
		} `json:"snippet"`
	} `json:"items"`
}

// Comments returns top-level comments posted at or after since, newest first.
// Each page costs one quota unit; maxPages bounds a runaway.
func (c *Client) Comments(ctx context.Context, since time.Time, maxPages int) ([]model.Mention, error) {
	if maxPages <= 0 {
		maxPages = 1
	}
	var out []model.Mention
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{
			"part":                         {"snippet"},
			"allThreadsRelatedToChannelId": {c.ChannelID},
			"order":                        {"time"},
			"maxResults":                   {"100"},
			"textFormat":                   {"plainText"},
			"key":                          {c.APIKey},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var body threadsPage
		if err := c.get(ctx, c.BaseURL+"/commentThreads?"+q.Encode(), &body); err != nil {
			return out, err
		}
		older := false
		for _, item := range body.Items {
			top := item.Snippet.TopLevelComment
			published, err := time.Parse(time.RFC3339, top.Snippet.PublishedAt)
			if err != nil {
				continue
			}
			if published.Before(since) {
				older = true
				continue
			}
			// The channel answering its own viewers is not a mention.
			if top.Snippet.AuthorChannelID.Value == c.ChannelID {
				continue
			}
			videoID := item.Snippet.VideoID
			if videoID == "" {
				videoID = top.Snippet.VideoID
			}
			link := "https://www.youtube.com/channel/" + url.PathEscape(c.ChannelID)
			if videoID != "" {
				link = "https://www.youtube.com/watch?" + url.Values{"v": {videoID}, "lc": {top.ID}}.Encode()
			}
			out = append(out, model.Mention{
				Platform:   model.PlatformYouTube,
				Kind:       "comment",
				ExternalID: top.ID,
				URL:        link,
				Author:     top.Snippet.AuthorDisplayName,
				Text:       top.Snippet.TextDisplay,
				Context:    videoID,
				PostedAt:   cliutil.FormatTime(published),
			})
		}
		if older || body.NextPageToken == "" {
			break
		}
		pageToken = body.NextPageToken
	}
	return out, nil
}

type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

func (c *Client) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// url.Error quotes the full URL, API key included, and this text ends
		// up in logs and chat. Keep only the cause.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("youtube request failed: %w", uerr.Err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var ae apiError
		if json.Unmarshal(body, &ae) == nil && ae.Error.Message != "" {
			reason := ""
			if len(ae.Error.Errors) > 0 {
				reason = " (" + ae.Error.Errors[0].Reason + ")"
			}
			return fmt.Errorf("youtube HTTP %d: %s%s", resp.StatusCode, ae.Error.Message, reason)
		}
		return &cliutil.HTTPError{Status: resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

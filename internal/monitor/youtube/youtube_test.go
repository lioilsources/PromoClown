package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func thread(id, video, authorChannel, text, published string) string {
	return fmt.Sprintf(`{"kind":"youtube#commentThread","id":"th-%s","snippet":{"channelId":"UCme","videoId":%q,
		"topLevelComment":{"id":%q,"snippet":{"authorDisplayName":"a-%s","authorChannelId":{"value":%q},
		"textDisplay":%q,"videoId":%q,"publishedAt":%q}},"totalReplyCount":0}}`,
		id, video, id, id, authorChannel, text, video, published)
}

func TestCommentsPaginationCutoffAndMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/commentThreads" || q.Get("key") != "KEY" || q.Get("allThreadsRelatedToChannelId") != "UCme" ||
			q.Get("textFormat") != "plainText" || q.Get("order") != "time" || q.Get("part") != "snippet" {
			t.Errorf("unexpected request %s", r.URL)
		}
		switch q.Get("pageToken") {
		case "":
			fmt.Fprintf(w, `{"nextPageToken":"p2","items":[%s,%s]}`,
				thread("c1", "vid1", "UCfan", "Great video", "2026-09-10T12:00:00Z"),
				thread("c2", "vid1", "UCme", "Thanks!", "2026-09-10T11:00:00Z"))
		case "p2":
			fmt.Fprintf(w, `{"nextPageToken":"p3","items":[%s,%s]}`,
				thread("c3", "vid2", "UCother", "How do I get it?", "2026-09-09T08:00:00Z"),
				thread("c4", "vid2", "UCother", "old", "2026-08-01T08:00:00Z"))
		default:
			t.Error("fetched a page past the cutoff")
			fmt.Fprint(w, `{"items":[]}`)
		}
	}))
	defer srv.Close()

	c := New("KEY", "UCme")
	c.BaseURL = srv.URL
	got, err := c.Comments(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments: %+v", len(got), got)
	}
	m := got[0]
	if m.Platform != "youtube" || m.Kind != "comment" || m.ExternalID != "c1" || m.Context != "vid1" ||
		m.URL != "https://www.youtube.com/watch?lc=c1&v=vid1" || m.Author != "a-c1" || m.Text != "Great video" ||
		m.PostedAt != "2026-09-10T12:00:00Z" {
		t.Errorf("mapping = %+v", m)
	}
	if got[1].ExternalID != "c3" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestErrorsNeverLeakTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"quota exceeded","errors":[{"reason":"quotaExceeded"}]}}`)
	}))
	c := New("SECRET-KEY", "UCme")
	c.BaseURL = srv.URL
	_, err := c.Comments(context.Background(), time.Time{}, 1)
	if err == nil || !strings.Contains(err.Error(), "quotaExceeded") {
		t.Errorf("api error = %v", err)
	}
	srv.Close()

	// Connection refused: net/http would normally quote the URL with the key.
	_, err = c.Comments(context.Background(), time.Time{}, 1)
	if err == nil || strings.Contains(err.Error(), "SECRET-KEY") {
		t.Errorf("transport error leaks the key: %v", err)
	}
}

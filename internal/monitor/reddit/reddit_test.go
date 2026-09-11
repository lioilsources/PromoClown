package reddit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const ua = "linux:promoclown:v1 (by /u/ol1n)"

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("cid", "csecret", "ol1n", "pw", ua)
	c.AuthURL, c.APIURL = srv.URL, srv.URL
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	return c, &slept
}

func tokenHandler(t *testing.T, w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "cid" || pass != "csecret" {
		t.Errorf("basic auth = %q %q %v", user, pass, ok)
	}
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	f := r.PostForm
	if f.Get("grant_type") != "password" || f.Get("username") != "ol1n" || f.Get("password") != "pw" ||
		f.Get("scope") != "read privatemessages" {
		t.Errorf("token form = %v", f)
	}
	fmt.Fprint(w, `{"access_token":"tok","token_type":"bearer","expires_in":3600,"scope":"read privatemessages"}`)
}

func link(name, sub, title, selftext string, created float64) string {
	return fmt.Sprintf(`{"kind":"t3","data":{"id":%q,"name":"t3_%s","subreddit":%q,"title":%q,"selftext":%q,
		"author":"dev42","permalink":"/r/%s/comments/%s/x/","url":"https://x","created_utc":%v}}`,
		name, name, sub, title, selftext, sub, name, created)
}

func TestSearchDedupesAndFilters(t *testing.T) {
	sep10 := float64(time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC).Unix())
	aug := float64(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Unix())
	requests := 0
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != ua {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		if r.URL.Path == "/api/v1/access_token" {
			tokenHandler(t, w, r)
			return
		}
		requests++
		if r.URL.Path != "/r/FlutterDev+indiegames/search" || r.Header.Get("Authorization") != "bearer tok" {
			t.Errorf("request %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		q := r.URL.Query()
		if q.Get("restrict_sr") != "1" || q.Get("sort") != "new" || q.Get("t") != "week" || q.Get("type") != "link" ||
			q.Get("raw_json") != "1" || q.Get("limit") != "100" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		switch q.Get("q") {
		case "kiran":
			w.Header().Set("X-Ratelimit-Remaining", "1.0")
			w.Header().Set("X-Ratelimit-Reset", "5")
			fmt.Fprintf(w, `{"kind":"Listing","data":{"after":null,"children":[%s,%s]}}`,
				link("aaa", "indiegames", "Kiran launch", strings.Repeat("é", 2500), sep10),
				link("old", "indiegames", "Old post", "", aug))
		case `"offline maps"`:
			fmt.Fprintf(w, `{"kind":"Listing","data":{"children":[%s,%s]}}`,
				link("aaa", "indiegames", "Kiran launch", "dup", sep10),
				link("bbb", "FlutterDev", "Offline maps in Flutter?", "", sep10))
		default:
			t.Errorf("q = %q", q.Get("q"))
		}
	})

	since := c.Now().Add(-7 * 24 * time.Hour)
	got, err := c.Search(context.Background(), []string{"r/FlutterDev", "indiegames"}, []string{"kiran", "offline maps"}, since, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || requests != 2 {
		t.Fatalf("got %d mentions in %d requests: %+v", len(got), requests, got)
	}
	m := got[0]
	if m.Platform != "reddit" || m.Kind != "search" || m.ExternalID != "t3_aaa" || m.Context != "indiegames" ||
		m.URL != "https://www.reddit.com/r/indiegames/comments/aaa/x/" || m.Author != "dev42" ||
		m.PostedAt != "2026-09-10T09:00:00Z" || !strings.HasPrefix(m.Text, "Kiran launch\n\n") {
		t.Errorf("mapping = %+v", m)
	}
	if n := len([]rune(m.Text)); n != len("Kiran launch\n\n")+2000+1 {
		t.Errorf("selftext not truncated to 2000 runes: %d", n)
	}
	if len(*slept) != 1 || (*slept)[0] != 5*time.Second {
		t.Errorf("rate limit sleeps = %v", *slept)
	}
}

func TestInbox(t *testing.T) {
	sep10 := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC).Unix()
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			tokenHandler(t, w, r)
			return
		}
		if r.URL.Path != "/message/inbox" || r.URL.Query().Get("mark") != "false" {
			t.Errorf("request %s", r.URL)
		}
		fmt.Fprintf(w, `{"kind":"Listing","data":{"children":[
			{"kind":"t1","data":{"id":"c1","name":"t1_c1","author":"fan","body":"is it on Android?","context":"/r/indiegames/comments/aaa/x/c1/?context=3","subject":"comment reply","subreddit":"indiegames","type":"comment_reply","was_comment":true,"created_utc":%d.0}},
			{"kind":"t4","data":{"id":"m1","name":"t4_m1","author":"press","body":"interview?","context":"","subject":"Kiran","subreddit":null,"was_comment":false,"created_utc":%d}},
			{"kind":"t4","data":{"id":"m0","name":"t4_m0","author":"x","body":"old","subject":"old","created_utc":1600000000}}
		]}}`, sep10, sep10)
	})
	got, err := c.Inbox(context.Background(), c.Now().Add(-7*24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Kind != "comment_reply" || got[0].Context != "indiegames" ||
		got[0].URL != "https://www.reddit.com/r/indiegames/comments/aaa/x/c1/?context=3" {
		t.Errorf("comment reply = %+v", got[0])
	}
	if got[1].Kind != "message" || got[1].Context != "Kiran" || got[1].URL != "https://www.reddit.com/message/messages/m1" ||
		got[1].ExternalID != "t4_m1" {
		t.Errorf("message = %+v", got[1])
	}
}

func TestReauthenticatesOnceOn401(t *testing.T) {
	tokens, calls := 0, 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			tokens++
			tokenHandler(t, w, r)
			return
		}
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":{"children":[]}}`)
	})
	if _, err := c.Inbox(context.Background(), time.Time{}, 10); err != nil {
		t.Fatal(err)
	}
	if tokens != 2 || calls != 2 {
		t.Errorf("tokens=%d calls=%d", tokens, calls)
	}
}

func TestBadCredentials(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error": "invalid_grant"}`)
	})
	_, err := c.Inbox(context.Background(), time.Time{}, 10)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %v", err)
	}
}

func TestTimeWindow(t *testing.T) {
	now := time.Now()
	for d, want := range map[time.Duration]string{
		12 * time.Hour: "day", 48 * time.Hour: "week", 20 * 24 * time.Hour: "month",
		100 * 24 * time.Hour: "year", 800 * 24 * time.Hour: "all",
	} {
		if got := TimeWindow(now.Add(-d), now); got != want {
			t.Errorf("TimeWindow(%v) = %s, want %s", d, got, want)
		}
	}
}

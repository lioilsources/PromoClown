package bluesky

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func notification(rkey, reason, created, indexed string) string {
	return fmt.Sprintf(`{"uri":"at://did:plc:fan/app.bsky.feed.post/%s","cid":"c","author":{"did":"did:plc:fan","handle":"fan.bsky.social"},
		"reason":%q,"reasonSubject":"at://did:plc:me/app.bsky.feed.post/root","record":{"$type":"app.bsky.feed.post","text":"hey @me %s","createdAt":%q},
		"isRead":false,"indexedAt":%q}`, rkey, reason, rkey, created, indexed)
}

func TestRefreshesExpiredSessionAndPages(t *testing.T) {
	var srv *httptest.Server
	listCalls := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			t.Error("createSession must not be called while refresh works")
		case "/xrpc/com.atproto.server.refreshSession":
			if r.Header.Get("Authorization") != "Bearer refresh-old" {
				t.Errorf("refresh auth = %q", r.Header.Get("Authorization"))
			}
			fmt.Fprintf(w, `{"accessJwt":"access-new","refreshJwt":"refresh-new","handle":"me.bsky.social","did":"did:plc:me",
				"didDoc":{"id":"did:plc:me","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"%s/"}]}}`, srv.URL)
		case "/xrpc/app.bsky.notification.listNotifications":
			listCalls++
			if r.Header.Get("Authorization") == "Bearer access-old" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"ExpiredToken","message":"Token has expired"}`)
				return
			}
			q := r.URL.Query()
			if len(q["reasons"]) != 3 || q.Get("limit") != "100" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			switch q.Get("cursor") {
			case "":
				fmt.Fprintf(w, `{"cursor":"c2","notifications":[%s,%s]}`,
					notification("p1", "mention", "2026-09-10T10:00:00.123Z", "2026-09-10T10:00:01.000Z"),
					notification("p2", "like", "2026-09-10T09:00:00Z", "2026-09-10T09:00:01Z"))
			case "c2":
				fmt.Fprintf(w, `{"cursor":"c3","notifications":[%s,%s]}`,
					notification("p3", "reply", "", "2026-09-09T08:00:00Z"),
					notification("p4", "quote", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z"))
			default:
				t.Error("fetched a page past the cutoff")
				fmt.Fprint(w, `{"notifications":[]}`)
			}
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer srv.Close()

	sessionFile := filepath.Join(t.TempDir(), "sub", "session.json")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	cached, _ := json.Marshal(Session{Identifier: "me.bsky.social", AccessJwt: "access-old", RefreshJwt: "refresh-old"})
	if err := os.WriteFile(sessionFile, cached, 0o600); err != nil {
		t.Fatal(err)
	}

	c := New(srv.URL, "me.bsky.social", "app-pw", sessionFile)
	got, err := c.Mentions(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d mentions: %+v", len(got), got)
	}
	m := got[0]
	if m.Platform != "bluesky" || m.Kind != "mention" || m.ExternalID != "at://did:plc:fan/app.bsky.feed.post/p1" ||
		m.URL != "https://bsky.app/profile/did:plc:fan/post/p1" || m.Author != "fan.bsky.social" ||
		m.Text != "hey @me p1" || m.Context != "at://did:plc:me/app.bsky.feed.post/root" || m.PostedAt != "2026-09-10T10:00:00Z" {
		t.Errorf("mapping = %+v", m)
	}
	if got[1].Kind != "reply" || got[1].PostedAt != "2026-09-09T08:00:00Z" {
		t.Errorf("createdAt fallback to indexedAt = %+v", got[1])
	}
	if listCalls != 3 {
		t.Errorf("listNotifications calls = %d, want 3 (expired, page 1, page 2)", listCalls)
	}

	info, err := os.Stat(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %v", info.Mode().Perm())
	}
	var saved Session
	b, _ := os.ReadFile(sessionFile)
	json.Unmarshal(b, &saved)
	if saved.AccessJwt != "access-new" || saved.PDS() != srv.URL || saved.Identifier != "me.bsky.social" {
		t.Errorf("saved session = %+v pds=%q", saved, saved.PDS())
	}
}

func TestLogsInWithoutCachedSession(t *testing.T) {
	created := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			created++
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["identifier"] != "me.bsky.social" || body["password"] != "app-pw" {
				t.Errorf("login body = %v", body)
			}
			fmt.Fprint(w, `{"accessJwt":"a","refreshJwt":"r","handle":"me.bsky.social","did":"did:plc:me"}`)
		case "/xrpc/app.bsky.notification.listNotifications":
			if r.Header.Get("Authorization") != "Bearer a" {
				t.Errorf("auth = %q", r.Header.Get("Authorization"))
			}
			fmt.Fprint(w, `{"notifications":[]}`)
		}
	}))
	defer srv.Close()

	// A cache from another account must be ignored.
	sessionFile := filepath.Join(t.TempDir(), "session.json")
	other, _ := json.Marshal(Session{Identifier: "someone.else", AccessJwt: "x", RefreshJwt: "y"})
	os.WriteFile(sessionFile, other, 0o600)

	c := New(srv.URL, "me.bsky.social", "app-pw", sessionFile)
	got, err := c.Mentions(context.Background(), time.Time{})
	if err != nil || len(got) != 0 || created != 1 {
		t.Fatalf("got %v, err %v, createSession calls %d", got, err, created)
	}
}

func TestPostURL(t *testing.T) {
	if got := PostURL("at://did:plc:abc/app.bsky.feed.post/3kx"); got != "https://bsky.app/profile/did:plc:abc/post/3kx" {
		t.Errorf("PostURL = %q", got)
	}
	if got := PostURL("at://did:plc:abc/app.bsky.feed.like/3kx"); got != "" {
		t.Errorf("non-post uri gave %q", got)
	}
}

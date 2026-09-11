package googleplay

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReviewsTokenFlowAndPagination(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	var srv *httptest.Server
	tokenCalls := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			tokenCalls++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
			}
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatalf("assertion has %d parts", len(parts))
			}
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
				t.Errorf("RS256 signature: %v", err)
			}
			var header, claims map[string]any
			decodeSeg(t, parts[0], &header)
			decodeSeg(t, parts[1], &claims)
			if header["alg"] != "RS256" || header["kid"] != "kid1" {
				t.Errorf("header = %v", header)
			}
			if claims["iss"] != "svc@proj.iam.gserviceaccount.com" || claims["aud"] != srv.URL+"/token" ||
				claims["scope"] != "https://www.googleapis.com/auth/androidpublisher" {
				t.Errorf("claims = %v", claims)
			}
			fmt.Fprint(w, `{"access_token":"at-1","expires_in":3599,"token_type":"Bearer"}`)
		case r.URL.Path == "/androidpublisher/v3/applications/com.ol1n.kiran/reviews":
			if r.Header.Get("Authorization") != "Bearer at-1" {
				t.Errorf("auth = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("token") == "" {
				fmt.Fprint(w, `{"reviews":[
					{"reviewId":"g1","authorName":"Jana","comments":[{"userComment":{"text":"\tSuper appka","starRating":5,"reviewerLanguage":"cs","appVersionName":"1.2.0","lastModified":{"seconds":"1789034400","nanos":0}}}]},
					{"reviewId":"g2","authorName":"Old","comments":[{"userComment":{"text":"old","starRating":2,"lastModified":{"seconds":"1700000000"}}}]}
				],"tokenPagination":{"nextPageToken":"p2"}}`)
				return
			}
			fmt.Fprint(w, `{"reviews":[
				{"reviewId":"g3","authorName":"Tom","comments":[{"developerComment":{"text":"thanks"}},{"userComment":{"text":"crash","originalText":"pád","starRating":1,"lastModified":{"seconds":1789030800}}}]}
			]}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(ServiceAccount{
		ClientEmail:  "svc@proj.iam.gserviceaccount.com",
		PrivateKey:   string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		PrivateKeyID: "kid1",
		TokenURI:     srv.URL + "/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL = srv.URL
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	got, err := c.Reviews(context.Background(), "com.ol1n.kiran", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d reviews: %+v", len(got), got)
	}
	g1 := got[0]
	if g1.Store != "googleplay" || g1.AppID != "com.ol1n.kiran" || g1.ExternalID != "g1" || g1.Rating != 5 ||
		g1.Text != "Super appka" || g1.Author != "Jana" || g1.Version != "1.2.0" || g1.Territory != "cs" ||
		g1.PostedAt != "2026-09-10T10:00:00Z" {
		t.Errorf("mapping = %+v", g1)
	}
	if got[1].Text != "pád" || got[1].Rating != 1 {
		t.Errorf("original text and numeric seconds = %+v", got[1])
	}
	if tokenCalls != 1 {
		t.Errorf("token fetched %d times, want 1", tokenCalls)
	}
}

func decodeSeg(t *testing.T, seg string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

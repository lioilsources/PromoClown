package appstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// verifyJWT checks what Apple checks: ES256 over R||S, the claims, the lifetime.
func verifyJWT(t *testing.T, pub *ecdsa.PublicKey, tok string) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	var header map[string]string
	var claims map[string]any
	decodeSeg(t, parts[0], &header)
	decodeSeg(t, parts[1], &claims)
	if header["alg"] != "ES256" || header["kid"] != "KEY123" || header["typ"] != "JWT" {
		t.Errorf("header = %v", header)
	}
	if claims["iss"] != "ISSUER" || claims["aud"] != "appstoreconnect-v1" {
		t.Errorf("claims = %v", claims)
	}
	if life := claims["exp"].(float64) - claims["iat"].(float64); life <= 0 || life > 20*60 {
		t.Errorf("token lifetime %v s", life)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature must be 64 raw bytes, got %d (%v)", len(sig), err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, sum[:], r, s) {
		t.Error("ES256 signature does not verify")
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

func TestReviewsPaginationAndCutoff(t *testing.T) {
	key := testKey(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, &key.PublicKey, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		review := func(id string, rating int, created string) string {
			return fmt.Sprintf(`{"type":"customerReviews","id":%q,"attributes":{"rating":%d,"title":"t%s","body":"b%s","reviewerNickname":"n%s","createdDate":%q,"territory":"CZE"}}`,
				id, rating, id, id, id, created)
		}
		switch {
		case r.URL.Path == "/page3":
			t.Error("fetched a page past the cutoff")
		case r.URL.Path == "/v1/apps/123/customerReviews" && r.URL.Query().Get("cursor") == "2":
			fmt.Fprintf(w, `{"data":[%s,%s],"links":{"self":"x","next":"%s/page3"}}`,
				review("r3", 4, "2026-09-09T23:00:00Z"), review("r4", 3, "2026-08-01T00:00:00Z"), srv.URL)
		case r.URL.Path == "/v1/apps/123/customerReviews":
			if r.URL.Query().Get("sort") != "-createdDate" || r.URL.Query().Get("limit") != "200" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"data":[%s,%s],"links":{"self":"x","next":"%s/v1/apps/123/customerReviews?cursor=2"}}`,
				review("r1", 5, "2026-09-10T10:00:00-07:00"), review("r2", 1, "2026-09-10T08:00:00Z"), srv.URL)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer srv.Close()

	c := New("KEY123", "ISSUER", key)
	c.BaseURL = srv.URL
	since := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	got, err := c.Reviews(context.Background(), "123", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d reviews: %+v", len(got), got)
	}
	r1 := got[0]
	if r1.Store != "appstore" || r1.AppID != "123" || r1.ExternalID != "r1" || r1.Rating != 5 ||
		r1.Title != "tr1" || r1.Text != "br1" || r1.Author != "nr1" || r1.Territory != "CZE" ||
		r1.PostedAt != "2026-09-10T17:00:00Z" || r1.Version != "" {
		t.Errorf("mapping = %+v", r1)
	}
}

func TestNextLinkMustStayOnHost(t *testing.T) {
	key := testKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"links":{"next":"https://evil.example/steal"}}`)
	}))
	defer srv.Close()
	c := New("KEY123", "ISSUER", key)
	c.BaseURL = srv.URL
	if _, err := c.Reviews(context.Background(), "123", time.Time{}); err == nil {
		t.Error("followed a next link to another host")
	}
}

func TestTokenIsReused(t *testing.T) {
	c := New("KEY123", "ISSUER", testKey(t))
	now := time.Now()
	c.Now = func() time.Time { return now }
	a, _ := c.Token()
	now = now.Add(10 * time.Minute)
	b, _ := c.Token()
	now = now.Add(9 * time.Minute)
	d, _ := c.Token()
	if a != b || a == d {
		t.Error("token should be reused for its lifetime and renewed before expiry")
	}
}

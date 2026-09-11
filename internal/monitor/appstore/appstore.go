// Package appstore reads customer reviews from the App Store Connect API.
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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const DefaultBaseURL = "https://api.appstoreconnect.apple.com"

const (
	// Apple rejects tokens living longer than 20 minutes for this resource.
	tokenLifetime = 19 * time.Minute
	maxPages      = 20
)

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	KeyID    string
	IssuerID string
	Key      *ecdsa.PrivateKey
	Now      func() time.Time

	token    string
	tokenExp time.Time
}

func New(keyID, issuerID string, key *ecdsa.PrivateKey) *Client {
	return &Client{
		BaseURL: DefaultBaseURL, HTTP: cliutil.HTTPClient(),
		KeyID: keyID, IssuerID: issuerID, Key: key, Now: time.Now,
	}
}

// LoadKey reads the .p8 file App Store Connect lets you download exactly once.
func LoadKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseKey(b)
}

// ParseKey parses a PKCS#8 PEM P-256 key.
func ParseKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in the App Store Connect key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse App Store Connect key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("App Store Connect key must be an EC P-256 key")
	}
	return ec, nil
}

// Token returns an ES256 JWT, reused until a minute before it expires.
func (c *Client) Token() (string, error) {
	now := c.Now()
	if c.token != "" && now.Before(c.tokenExp.Add(-time.Minute)) {
		return c.token, nil
	}
	exp := now.Add(tokenLifetime)
	header, err := cliutil.JWTSegment(map[string]string{"alg": "ES256", "kid": c.KeyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := cliutil.JWTSegment(map[string]any{
		"iss": c.IssuerID, "iat": now.Unix(), "exp": exp.Unix(), "aud": "appstoreconnect-v1",
	})
	if err != nil {
		return "", err
	}
	input := header + "." + claims
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, c.Key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS wants R||S as two fixed 32-byte integers, not the ASN.1 DER that
	// SignASN1 produces; Apple answers DER signatures with a bare 401.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	c.token = input + "." + base64.RawURLEncoding.EncodeToString(sig)
	c.tokenExp = exp
	return c.token, nil
}

type reviewsPage struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Rating           int    `json:"rating"`
			Title            string `json:"title"`
			Body             string `json:"body"`
			ReviewerNickname string `json:"reviewerNickname"`
			CreatedDate      string `json:"createdDate"`
			Territory        string `json:"territory"`
		} `json:"attributes"`
	} `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

// Reviews returns the app's reviews created at or after since. The API has no
// app version on a review, so Version stays empty.
func (c *Client) Reviews(ctx context.Context, appID string, since time.Time) ([]model.Review, error) {
	q := url.Values{"sort": {"-createdDate"}, "limit": {"200"}}
	next := c.BaseURL + "/v1/apps/" + url.PathEscape(appID) + "/customerReviews?" + q.Encode()
	var out []model.Review
	for page := 0; next != "" && page < maxPages; page++ {
		var body reviewsPage
		if err := c.get(ctx, next, &body); err != nil {
			return out, fmt.Errorf("app %s: %w", appID, err)
		}
		next = body.Links.Next
		// The bearer token follows links.next, so it must stay on Apple's host.
		if next != "" && !strings.HasPrefix(next, c.BaseURL+"/") {
			return out, fmt.Errorf("app %s: refusing to follow next link off %s", appID, c.BaseURL)
		}
		for _, d := range body.Data {
			created, err := time.Parse(time.RFC3339, d.Attributes.CreatedDate)
			if err != nil {
				continue
			}
			if created.Before(since) {
				next = "" // sorted newest first: everything after this is older
				break
			}
			out = append(out, model.Review{
				Store:      model.StoreAppStore,
				AppID:      appID,
				ExternalID: d.ID,
				Rating:     d.Attributes.Rating,
				Title:      d.Attributes.Title,
				Text:       d.Attributes.Body,
				Author:     d.Attributes.ReviewerNickname,
				Territory:  d.Attributes.Territory,
				PostedAt:   cliutil.FormatTime(created),
			})
		}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, u string, v any) error {
	tok, err := c.Token()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cliutil.CheckResponse(resp); err != nil {
		return err
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

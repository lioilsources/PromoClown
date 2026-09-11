// Package googleplay reads reviews from the Google Play Developer API.
//
// The API only returns reviews created or modified in the last week, so the
// monitor has to run at least that often or reviews are lost for good.
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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const (
	DefaultBaseURL  = "https://androidpublisher.googleapis.com"
	DefaultTokenURL = "https://oauth2.googleapis.com/token"

	scope    = "https://www.googleapis.com/auth/androidpublisher"
	maxPages = 50
)

// ServiceAccount is the subset of a Google service account key file we use.
type ServiceAccount struct {
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

func LoadServiceAccount(path string) (ServiceAccount, error) {
	var acct ServiceAccount
	b, err := os.ReadFile(path)
	if err != nil {
		return acct, err
	}
	if err := json.Unmarshal(b, &acct); err != nil {
		return acct, fmt.Errorf("service account %s: %w", path, err)
	}
	return acct, nil
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Account ServiceAccount
	Now     func() time.Time

	key      *rsa.PrivateKey
	token    string
	tokenExp time.Time
}

func New(acct ServiceAccount) (*Client, error) {
	if acct.ClientEmail == "" || acct.PrivateKey == "" {
		return nil, errors.New("service account key needs client_email and private_key")
	}
	key, err := parseKey(acct.PrivateKey)
	if err != nil {
		return nil, err
	}
	if acct.TokenURI == "" {
		acct.TokenURI = DefaultTokenURL
	}
	return &Client{BaseURL: DefaultBaseURL, HTTP: cliutil.HTTPClient(), Account: acct, Now: time.Now, key: key}, nil
}

func parseKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no PEM block in service account private_key")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("service account key is not RSA")
		}
		return rk, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// accessToken runs the service-account JWT bearer flow, caching the result.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	now := c.Now()
	if c.token != "" && now.Before(c.tokenExp.Add(-time.Minute)) {
		return c.token, nil
	}
	header, err := cliutil.JWTSegment(map[string]string{"alg": "RS256", "typ": "JWT", "kid": c.Account.PrivateKeyID})
	if err != nil {
		return "", err
	}
	claims, err := cliutil.JWTSegment(map[string]any{
		"iss": c.Account.ClientEmail, "scope": scope, "aud": c.Account.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	input := header + "." + claims
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {input + "." + base64.RawURLEncoding.EncodeToString(sig)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Account.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := cliutil.CheckResponse(resp); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("token: no access_token in response")
	}
	c.token = tok.AccessToken
	c.tokenExp = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.token, nil
}

// seconds accepts a number or, as the API actually sends it, a numeric string.
type seconds int64

func (s *seconds) UnmarshalJSON(b []byte) error {
	v := strings.Trim(string(b), `"`)
	if v == "" || v == "null" {
		*s = 0
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	*s = seconds(n)
	return err
}

type userComment struct {
	Text             string `json:"text"`
	OriginalText     string `json:"originalText"`
	StarRating       int    `json:"starRating"`
	ReviewerLanguage string `json:"reviewerLanguage"`
	AppVersionName   string `json:"appVersionName"`
	LastModified     struct {
		Seconds seconds `json:"seconds"`
	} `json:"lastModified"`
}

type reviewsPage struct {
	Reviews []struct {
		ReviewID   string `json:"reviewId"`
		AuthorName string `json:"authorName"`
		Comments   []struct {
			UserComment *userComment `json:"userComment"`
		} `json:"comments"`
	} `json:"reviews"`
	TokenPagination *struct {
		NextPageToken string `json:"nextPageToken"`
	} `json:"tokenPagination"`
}

// Reviews returns the package's reviews last modified at or after since.
// Play has no territory on a review; the reviewer's language goes there.
func (c *Client) Reviews(ctx context.Context, pkg string, since time.Time) ([]model.Review, error) {
	var out []model.Review
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{"maxResults": {"100"}}
		if pageToken != "" {
			q.Set("token", pageToken)
		}
		u := c.BaseURL + "/androidpublisher/v3/applications/" + url.PathEscape(pkg) + "/reviews?" + q.Encode()
		var body reviewsPage
		if err := c.get(ctx, u, &body); err != nil {
			return out, fmt.Errorf("package %s: %w", pkg, err)
		}
		for _, r := range body.Reviews {
			var uc *userComment
			for _, cm := range r.Comments {
				if cm.UserComment != nil {
					uc = cm.UserComment
					break
				}
			}
			if uc == nil {
				continue
			}
			modified := time.Unix(int64(uc.LastModified.Seconds), 0)
			if modified.Before(since) {
				continue
			}
			text := uc.Text
			if uc.OriginalText != "" {
				text = uc.OriginalText
			}
			out = append(out, model.Review{
				Store:      model.StoreGooglePlay,
				AppID:      pkg,
				ExternalID: r.ReviewID,
				Rating:     uc.StarRating,
				Text:       strings.TrimSpace(text),
				Author:     r.AuthorName,
				Version:    uc.AppVersionName,
				Territory:  uc.ReviewerLanguage,
				PostedAt:   cliutil.FormatTime(modified),
			})
		}
		if body.TokenPagination == nil || body.TokenPagination.NextPageToken == "" {
			break
		}
		pageToken = body.TokenPagination.NextPageToken
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, u string, v any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
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

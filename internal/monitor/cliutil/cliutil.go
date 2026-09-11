// Package cliutil holds the few helpers the monitor CLIs share.
package cliutil

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// HTTPClient is what every monitor talks through. A hung API must not stall
// the heartbeat that runs the monitors.
func HTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// ParseSince reads "48h" / "7d" style durations or an RFC 3339 time.
func ParseSince(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return now.Add(-48 * time.Hour), nil
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && days >= 0 {
			return now.Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: use a duration like 48h or 7d, or an RFC 3339 time", v)
}

// FormatTime renders a platform timestamp the way promo-api stores it.
func FormatTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// WriteJSON prints items as a JSON array, "[]" when empty, so the importer on
// the other end of the pipe never has to cope with null.
func WriteJSON[T any](w io.Writer, items []T) error {
	if items == nil {
		items = []T{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

// SplitList splits a comma-separated flag or env value, dropping blanks.
func SplitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Env prefers an explicit flag value over the environment.
func Env(flagValue, name string) string {
	if flagValue != "" {
		return flagValue
	}
	return strings.TrimSpace(os.Getenv(name))
}

// Var is one credential of a source, by env name.
type Var struct{ Name, Value string }

// Missing names the empty vars. All missing means the source is simply not
// set up; some missing means it is set up wrong, which is worth failing on.
func Missing(vars ...Var) []string {
	var out []string
	for _, v := range vars {
		if v.Value == "" {
			out = append(out, v.Name)
		}
	}
	return out
}

// Truncate cuts s to at most n runes.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos] + "…"
		}
		i++
	}
	return s
}

// HTTPError is a non-2xx answer with the start of its body for context.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// CheckResponse turns a non-2xx response into an *HTTPError.
func CheckResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

// JWTSegment is one base64url JSON segment of a JWT, without padding.
func JWTSegment(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

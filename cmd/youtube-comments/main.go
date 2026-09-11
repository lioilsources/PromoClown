// Command youtube-comments prints recent comments on the channel's videos as a
// JSON array for `promo mentions import`. Read-only.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
	"github.com/lioilsources/promoclown/internal/monitor/youtube"
)

const usage = `usage: youtube-comments fetch [--since 48h] [--max-pages 3]

Env: YOUTUBE_API_KEY, YOUTUBE_CHANNEL_ID (UC…). Each page costs one quota unit.
Without credentials it prints [] and exits 0.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "fetch" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("youtube-comments fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "48h", "only comments newer than this (48h, 7d or RFC 3339)")
	maxPages := fs.Int("max-pages", 3, "stop after this many pages of 100")
	channel := fs.String("channel", "", "channel id (default $YOUTUBE_CHANNEL_ID)")
	baseURL := fs.String("api-url", youtube.DefaultBaseURL, "YouTube Data API base URL")
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cutoff, err := cliutil.ParseSince(*since, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "youtube-comments: %v\n", err)
		return 2
	}

	// The API key is env-only: a flag would show it in ps output.
	key := cliutil.Env("", "YOUTUBE_API_KEY")
	channelID := cliutil.Env(*channel, "YOUTUBE_CHANNEL_ID")
	vars := []cliutil.Var{{Name: "YOUTUBE_API_KEY", Value: key}, {Name: "YOUTUBE_CHANNEL_ID", Value: channelID}}
	switch missing := cliutil.Missing(vars...); {
	case len(missing) == len(vars):
		fmt.Fprintln(stderr, "youtube-comments: YouTube not configured, skipping")
		cliutil.WriteJSON[any](stdout, nil)
		return 0
	case len(missing) > 0:
		fmt.Fprintf(stderr, "youtube-comments: missing %s\n", strings.Join(missing, ", "))
		cliutil.WriteJSON[any](stdout, nil)
		return 1
	}

	c := youtube.New(key, channelID)
	c.BaseURL = strings.TrimRight(*baseURL, "/")
	comments, err := c.Comments(ctx, cutoff, *maxPages)
	if werr := cliutil.WriteJSON(stdout, comments); werr != nil {
		fmt.Fprintf(stderr, "youtube-comments: %v\n", werr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "youtube-comments: %v\n", err)
		return 1
	}
	return 0
}

// Command reddit-monitor prints Reddit keyword hits or inbox items as a JSON
// array for `promo mentions import`. Read-only: it cannot post, vote or mark
// messages read.
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

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
	"github.com/lioilsources/promoclown/internal/monitor/reddit"
)

const usage = `usage:
  reddit-monitor search --sub FlutterDev,indiegames --keywords "kiran,offline maps" [--since 7d] [--limit 100]
  reddit-monitor inbox [--since 7d] [--limit 100]

Env: REDDIT_CLIENT_ID, REDDIT_CLIENT_SECRET, REDDIT_USERNAME, REDDIT_PASSWORD,
     REDDIT_USER_AGENT (e.g. "linux:promoclown:v1 (by /u/name)")
Script-app access needs approval under Reddit's Responsible Builder Policy.
Without credentials it prints [] and exits 0.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "search" && args[0] != "inbox") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("reddit-monitor "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "7d", "only items newer than this (48h, 7d or RFC 3339)")
	limit := fs.Int("limit", 100, "results per request, at most 100")
	subs := fs.String("sub", "", "comma-separated subreddits (search)")
	keywords := fs.String("keywords", "", "comma-separated keywords; multi-word ones are searched as phrases (search)")
	apiURL := fs.String("api-url", reddit.DefaultAPIURL, "Reddit OAuth API base URL")
	authURL := fs.String("auth-url", reddit.DefaultAuthURL, "Reddit token endpoint base URL")
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cutoff, err := cliutil.ParseSince(*since, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "reddit-monitor: %v\n", err)
		return 2
	}
	if cmd == "search" && (cliutil.SplitList(*subs) == nil || cliutil.SplitList(*keywords) == nil) {
		fmt.Fprintln(stderr, "reddit-monitor: search needs --sub and --keywords")
		return 2
	}

	vars := []cliutil.Var{
		{Name: "REDDIT_CLIENT_ID", Value: cliutil.Env("", "REDDIT_CLIENT_ID")},
		{Name: "REDDIT_CLIENT_SECRET", Value: cliutil.Env("", "REDDIT_CLIENT_SECRET")},
		{Name: "REDDIT_USERNAME", Value: cliutil.Env("", "REDDIT_USERNAME")},
		{Name: "REDDIT_PASSWORD", Value: cliutil.Env("", "REDDIT_PASSWORD")},
		{Name: "REDDIT_USER_AGENT", Value: cliutil.Env("", "REDDIT_USER_AGENT")},
	}
	switch missing := cliutil.Missing(vars...); {
	case len(missing) == len(vars):
		fmt.Fprintln(stderr, "reddit-monitor: Reddit not configured, skipping")
		cliutil.WriteJSON[model.Mention](stdout, nil)
		return 0
	case len(missing) > 0:
		fmt.Fprintf(stderr, "reddit-monitor: missing %s\n", strings.Join(missing, ", "))
		cliutil.WriteJSON[model.Mention](stdout, nil)
		return 1
	}

	c := reddit.New(vars[0].Value, vars[1].Value, vars[2].Value, vars[3].Value, vars[4].Value)
	c.APIURL = strings.TrimRight(*apiURL, "/")
	c.AuthURL = strings.TrimRight(*authURL, "/")

	var items []model.Mention
	if cmd == "search" {
		items, err = c.Search(ctx, cliutil.SplitList(*subs), cliutil.SplitList(*keywords), cutoff, *limit)
	} else {
		items, err = c.Inbox(ctx, cutoff, *limit)
	}
	if werr := cliutil.WriteJSON(stdout, items); werr != nil {
		fmt.Fprintf(stderr, "reddit-monitor: %v\n", werr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "reddit-monitor: %v\n", err)
		return 1
	}
	return 0
}

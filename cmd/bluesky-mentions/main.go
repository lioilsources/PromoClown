// Command bluesky-mentions prints recent mentions, replies and quotes of the
// Bluesky account as a JSON array for `promo mentions import`. Read-only.
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
	"github.com/lioilsources/promoclown/internal/monitor/bluesky"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const usage = `usage: bluesky-mentions fetch [--since 48h]

Env: BLUESKY_IDENTIFIER (handle or e-mail), BLUESKY_APP_PASSWORD,
     BLUESKY_SERVICE (default https://bsky.social),
     BLUESKY_SESSION_FILE (default $XDG_CACHE_HOME or ~/.cache /promoclown/bluesky-session.json)
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
	fs := flag.NewFlagSet("bluesky-mentions fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "48h", "only posts newer than this (48h, 7d or RFC 3339)")
	identifier := fs.String("identifier", "", "handle or e-mail (default $BLUESKY_IDENTIFIER)")
	service := fs.String("service", "", "entryway URL (default $BLUESKY_SERVICE or https://bsky.social)")
	sessionFile := fs.String("session-file", "", "session cache (default $BLUESKY_SESSION_FILE)")
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cutoff, err := cliutil.ParseSince(*since, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "bluesky-mentions: %v\n", err)
		return 2
	}

	id := cliutil.Env(*identifier, "BLUESKY_IDENTIFIER")
	// The app password is env-only: a flag would show it in ps output.
	password := cliutil.Env("", "BLUESKY_APP_PASSWORD")
	vars := []cliutil.Var{{Name: "BLUESKY_IDENTIFIER", Value: id}, {Name: "BLUESKY_APP_PASSWORD", Value: password}}
	switch missing := cliutil.Missing(vars...); {
	case len(missing) == len(vars):
		fmt.Fprintln(stderr, "bluesky-mentions: Bluesky not configured, skipping")
		cliutil.WriteJSON[model.Mention](stdout, nil)
		return 0
	case len(missing) > 0:
		fmt.Fprintf(stderr, "bluesky-mentions: missing %s\n", strings.Join(missing, ", "))
		cliutil.WriteJSON[model.Mention](stdout, nil)
		return 1
	}

	file := cliutil.Env(*sessionFile, "BLUESKY_SESSION_FILE")
	if file == "" {
		file = bluesky.DefaultSessionFile()
	}
	c := bluesky.New(cliutil.Env(*service, "BLUESKY_SERVICE"), id, password, file)
	items, err := c.Mentions(ctx, cutoff)
	if werr := cliutil.WriteJSON(stdout, items); werr != nil {
		fmt.Fprintf(stderr, "bluesky-mentions: %v\n", werr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "bluesky-mentions: %v\n", err)
		return 1
	}
	return 0
}

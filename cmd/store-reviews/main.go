// Command store-reviews prints recent App Store and Google Play reviews as a
// JSON array for `promo reviews import`. Read-only: it never answers a review.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/monitor/appstore"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
	"github.com/lioilsources/promoclown/internal/monitor/googleplay"
)

const usage = `usage: store-reviews fetch [--since 48h] [--store all|appstore|googleplay]

App Store Connect:  ASC_KEY_ID, ASC_ISSUER_ID, ASC_PRIVATE_KEY_PATH (.p8), ASC_APP_IDS (comma list)
Google Play:        GOOGLE_PLAY_SERVICE_ACCOUNT_JSON (key file path), GOOGLE_PLAY_PACKAGES (comma list)

A store without credentials is skipped unless it was selected with --store.
Google Play only returns reviews from the last seven days.
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
	fs := flag.NewFlagSet("store-reviews fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "48h", "only reviews newer than this (48h, 7d or RFC 3339)")
	store := fs.String("store", "all", "all, appstore or googleplay")
	ascKeyID := fs.String("asc-key-id", "", "App Store Connect key id (default $ASC_KEY_ID)")
	ascIssuer := fs.String("asc-issuer-id", "", "App Store Connect issuer id (default $ASC_ISSUER_ID)")
	ascKey := fs.String("asc-key", "", "path to the .p8 key (default $ASC_PRIVATE_KEY_PATH)")
	ascApps := fs.String("asc-apps", "", "comma-separated App Store app ids (default $ASC_APP_IDS)")
	ascURL := fs.String("asc-url", appstore.DefaultBaseURL, "App Store Connect API base URL")
	playKey := fs.String("play-key", "", "service account JSON path (default $GOOGLE_PLAY_SERVICE_ACCOUNT_JSON)")
	playPkgs := fs.String("play-packages", "", "comma-separated package names (default $GOOGLE_PLAY_PACKAGES)")
	playURL := fs.String("play-url", googleplay.DefaultBaseURL, "Google Play Developer API base URL")
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *store != "all" && *store != model.StoreAppStore && *store != model.StoreGooglePlay {
		fmt.Fprintf(stderr, "store-reviews: --store must be all, appstore or googleplay\n")
		return 2
	}
	cutoff, err := cliutil.ParseSince(*since, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "store-reviews: %v\n", err)
		return 2
	}

	var reviews []model.Review
	failed := false
	fail := func(format string, a ...any) {
		fmt.Fprintf(stderr, "store-reviews: "+format+"\n", a...)
		failed = true
	}
	// ready reports whether a source can run; a half-configured source, or an
	// unconfigured one the user asked for by name, is a failure.
	ready := func(name, selector string, vars ...cliutil.Var) bool {
		missing := cliutil.Missing(vars...)
		switch {
		case len(missing) == 0:
			return true
		case len(missing) == len(vars) && *store == "all":
			fmt.Fprintf(stderr, "store-reviews: %s not configured, skipping\n", name)
		default:
			fail("%s: missing %s", name, strings.Join(missing, ", "))
		}
		return false
	}

	if *store == "all" || *store == model.StoreAppStore {
		keyID := cliutil.Env(*ascKeyID, "ASC_KEY_ID")
		issuer := cliutil.Env(*ascIssuer, "ASC_ISSUER_ID")
		keyPath := cliutil.Env(*ascKey, "ASC_PRIVATE_KEY_PATH")
		apps := cliutil.Env(*ascApps, "ASC_APP_IDS")
		if ready("App Store", model.StoreAppStore,
			cliutil.Var{Name: "ASC_KEY_ID", Value: keyID}, cliutil.Var{Name: "ASC_ISSUER_ID", Value: issuer},
			cliutil.Var{Name: "ASC_PRIVATE_KEY_PATH", Value: keyPath}, cliutil.Var{Name: "ASC_APP_IDS", Value: apps}) {
			got, err := fetchAppStore(ctx, *ascURL, keyID, issuer, keyPath, cliutil.SplitList(apps), cutoff)
			reviews = append(reviews, got...)
			if err != nil {
				fail("App Store: %v", err)
			}
		}
	}

	if *store == "all" || *store == model.StoreGooglePlay {
		keyPath := cliutil.Env(*playKey, "GOOGLE_PLAY_SERVICE_ACCOUNT_JSON")
		pkgs := cliutil.Env(*playPkgs, "GOOGLE_PLAY_PACKAGES")
		if ready("Google Play", model.StoreGooglePlay,
			cliutil.Var{Name: "GOOGLE_PLAY_SERVICE_ACCOUNT_JSON", Value: keyPath},
			cliutil.Var{Name: "GOOGLE_PLAY_PACKAGES", Value: pkgs}) {
			got, err := fetchGooglePlay(ctx, *playURL, keyPath, cliutil.SplitList(pkgs), cutoff)
			reviews = append(reviews, got...)
			if err != nil {
				fail("Google Play: %v", err)
			}
		}
	}

	// Whatever did arrive is still printed, so one broken store does not hold
	// back the other's reviews; the exit code still reports the failure.
	if err := cliutil.WriteJSON(stdout, reviews); err != nil {
		fmt.Fprintf(stderr, "store-reviews: %v\n", err)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

func fetchAppStore(ctx context.Context, baseURL, keyID, issuer, keyPath string, apps []string, since time.Time) ([]model.Review, error) {
	key, err := appstore.LoadKey(keyPath)
	if err != nil {
		return nil, err
	}
	c := appstore.New(keyID, issuer, key)
	c.BaseURL = strings.TrimRight(baseURL, "/")
	var out []model.Review
	var errs []error
	for _, app := range apps {
		got, err := c.Reviews(ctx, app, since)
		out = append(out, got...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

func fetchGooglePlay(ctx context.Context, baseURL, keyPath string, pkgs []string, since time.Time) ([]model.Review, error) {
	acct, err := googleplay.LoadServiceAccount(keyPath)
	if err != nil {
		return nil, err
	}
	c, err := googleplay.New(acct)
	if err != nil {
		return nil, err
	}
	c.BaseURL = strings.TrimRight(baseURL, "/")
	var out []model.Review
	var errs []error
	for _, pkg := range pkgs {
		got, err := c.Reviews(ctx, pkg, since)
		out = append(out, got...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

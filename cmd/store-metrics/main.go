// Command store-metrics prints App Store numbers per app and day as a JSON
// array: downloads and proceeds from Sales and Trends, and impressions, page
// views, conversion, sessions and crashes from the Analytics Reports.
// Read-only, except `request-analytics`, which asks Apple once per app to start
// generating analytics reports and needs an Admin key.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/lioilsources/promoclown/internal/monitor/appstore"
	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

const usage = `usage: store-metrics fetch [--days 7] [--sales=true] [--analytics=true]
       store-metrics request-analytics          (once per app, Admin key)

App Store Connect: ASC_KEY_ID, ASC_ISSUER_ID, ASC_PRIVATE_KEY_PATH (.p8), ASC_APP_IDS (comma list),
                   ASC_VENDOR_NUMBER (Payments and Financial Reports) for sales.
fetch needs a key with the Sales and Reports role (Finance or Admin also work).

Days run up to yesterday (UTC). A field missing from a day means no data for it
(report not generated yet, analytics not requested, or no access), not zero.
Analytics reports appear 1–2 days after request-analytics and lag about a day.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, time.Now())
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, now time.Time) int {
	if len(args) == 0 || (args[0] != "fetch" && args[0] != "request-analytics") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("store-metrics "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	days := fs.Int("days", 7, "how many days back, ending yesterday")
	sales := fs.Bool("sales", true, "read Sales and Trends")
	analytics := fs.Bool("analytics", true, "read Analytics Reports")
	keyID := fs.String("asc-key-id", "", "App Store Connect key id (default $ASC_KEY_ID)")
	issuer := fs.String("asc-issuer-id", "", "issuer id (default $ASC_ISSUER_ID)")
	keyPath := fs.String("asc-key", "", "path to the .p8 key (default $ASC_PRIVATE_KEY_PATH)")
	apps := fs.String("asc-apps", "", "comma-separated app ids (default $ASC_APP_IDS)")
	vendor := fs.String("vendor", "", "vendor number (default $ASC_VENDOR_NUMBER)")
	baseURL := fs.String("asc-url", appstore.DefaultBaseURL, "App Store Connect API base URL")
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *days < 1 || *days > 60 {
		fmt.Fprintln(stderr, "store-metrics: --days must be 1–60")
		return 2
	}
	vars := []cliutil.Var{
		{Name: "ASC_KEY_ID", Value: cliutil.Env(*keyID, "ASC_KEY_ID")},
		{Name: "ASC_ISSUER_ID", Value: cliutil.Env(*issuer, "ASC_ISSUER_ID")},
		{Name: "ASC_PRIVATE_KEY_PATH", Value: cliutil.Env(*keyPath, "ASC_PRIVATE_KEY_PATH")},
		{Name: "ASC_APP_IDS", Value: cliutil.Env(*apps, "ASC_APP_IDS")},
	}
	if missing := cliutil.Missing(vars...); len(missing) > 0 {
		if len(missing) == len(vars) && cmd == "fetch" {
			fmt.Fprintln(stderr, "store-metrics: App Store not configured, skipping")
			_ = cliutil.WriteJSON[appstore.DayMetrics](stdout, nil)
			return 0
		}
		fmt.Fprintf(stderr, "store-metrics: missing %s\n", strings.Join(missing, ", "))
		return 1
	}
	key, err := appstore.LoadKey(vars[2].Value)
	if err != nil {
		fmt.Fprintf(stderr, "store-metrics: %v\n", err)
		return 1
	}
	c := appstore.New(vars[0].Value, vars[1].Value, key)
	c.BaseURL = strings.TrimRight(*baseURL, "/")
	appIDs := cliutil.SplitList(vars[3].Value)

	if cmd == "request-analytics" {
		failed := false
		for _, app := range appIDs {
			id, created, err := c.RequestAnalytics(ctx, app)
			switch {
			case err != nil:
				fmt.Fprintf(stderr, "app %s: %v\n", app, err)
				failed = true
			case created:
				fmt.Fprintf(stdout, "app %s: requested (%s); first reports in 1–2 days\n", app, id)
			default:
				fmt.Fprintf(stdout, "app %s: already requested (%s)\n", app, id)
			}
		}
		if failed {
			return 1
		}
		return 0
	}

	dates := appstore.Dates(now, *days)
	from, to := dates[0], dates[len(dates)-1]
	failed := false
	fail := func(format string, a ...any) {
		fmt.Fprintf(stderr, "store-metrics: "+format+"\n", a...)
		failed = true
	}
	byApp := map[string]map[string]*appstore.DayMetrics{}
	day := func(app, date string) *appstore.DayMetrics {
		if byApp[app] == nil {
			byApp[app] = map[string]*appstore.DayMetrics{}
		}
		m := byApp[app][date]
		if m == nil {
			m = &appstore.DayMetrics{AppID: app, Date: date}
			byApp[app][date] = m
		}
		return m
	}
	names := map[string]string{}
	skus := map[string]string{} // SKU → app id, for in-app purchase rows
	for _, app := range appIDs {
		sku, name, err := c.AppSKU(ctx, app)
		if err != nil {
			fail("app %s: %v", app, err)
			continue
		}
		names[app] = name
		if sku != "" {
			skus[sku] = app
		}
	}

	if *sales {
		v := cliutil.Env(*vendor, "ASC_VENDOR_NUMBER")
		if v == "" {
			fail("sales: missing ASC_VENDOR_NUMBER")
		} else {
			for _, date := range dates {
				rows, ok, err := c.SalesDay(ctx, v, date)
				if err != nil {
					fail("%v", err)
					if isAuth(err) {
						break // the same key fails every day the same way
					}
					continue
				}
				if !ok {
					continue
				}
				for _, app := range appIDs {
					appstore.AddSales(day(app, date), rows, skus)
				}
			}
		}
	}

	if *analytics {
		for _, app := range appIDs {
			if byApp[app] == nil {
				byApp[app] = map[string]*appstore.DayMetrics{}
			}
			err := c.Analytics(ctx, app, from, to, byApp[app])
			if errors.Is(err, appstore.ErrNoAnalyticsRequest) {
				fmt.Fprintf(stderr, "store-metrics: app %s: %v\n", app, err)
				continue
			}
			if err != nil {
				fail("app %s analytics: %v", app, err)
				if isAuth(err) {
					fmt.Fprintln(stderr, "store-metrics: the key needs the Sales and Reports role (Customer Support only reads reviews)")
				}
			}
		}
	}

	var out []appstore.DayMetrics
	for _, app := range appIDs {
		for _, m := range byApp[app] {
			if m.AppName == "" {
				m.AppName = names[app]
			}
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AppID != out[j].AppID {
			return out[i].AppID < out[j].AppID
		}
		return out[i].Date < out[j].Date
	})
	if err := cliutil.WriteJSON(stdout, out); err != nil {
		fmt.Fprintf(stderr, "store-metrics: %v\n", err)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

func isAuth(err error) bool {
	var he *cliutil.HTTPError
	return errors.As(err, &he) && (he.Status == 401 || he.Status == 403)
}

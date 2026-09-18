// Command promo-restyle renders one photo in several painter or era styles on
// ComfyUI, keeping the pose and the face, and prints where the files landed.
// It only produces files and JSON; feeding them to promo-api is someone else's
// job.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lioilsources/promoclown/internal/restyle"
)

const usage = `usage: promo-restyle -image photo.jpg -out dir [-n 4] [-styles id,id]

Renders one variant per style, sequentially. Prints
{"source":..., "results":[{"style","label","path","seed"}]} to stdout.

Env: COMFY_URL (default ` + restyle.DefaultBaseURL + `)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type output struct {
	Source  string           `json:"source"`
	Results []restyle.Result `json:"results"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("promo-restyle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	image := fs.String("image", "", "source photo (required)")
	out := fs.String("out", "", "directory for the rendered variants (required)")
	n := fs.Int("n", 4, "how many styles to draw")
	styleIDs := fs.String("styles", "", "comma-separated style ids to render instead of a random draw")
	seed := fs.Uint64("seed", 0, "seed for the style draw and the generation seeds (0 = random)")
	comfy := fs.String("comfy", "", "ComfyUI base URL (default $COMFY_URL or "+restyle.DefaultBaseURL+")")
	timeout := fs.Duration("timeout", restyle.DefaultJobTimeout, "per-variant timeout")
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *image == "" || *out == "" {
		fmt.Fprint(stderr, usage)
		return 2
	}

	styles, err := chooseStyles(*styleIDs, *n, *seed)
	if err != nil {
		fmt.Fprintf(stderr, "promo-restyle: %v\n", err)
		return 2
	}

	base := *comfy
	if base == "" {
		base = os.Getenv("COMFY_URL")
	}
	if base == "" {
		base = restyle.DefaultBaseURL
	}

	c := restyle.New(base)
	c.JobTimeout = *timeout
	if *seed != 0 {
		c.Rand = restyle.NewRand(*seed)
	}
	started := time.Now()
	c.Progress = func(index, total int, s restyle.Style, done bool) {
		if done {
			fmt.Fprintf(stderr, "[%d/%d] %s done in %s\n", index+1, total, s.ID, time.Since(started).Round(time.Second))
			return
		}
		started = time.Now()
		fmt.Fprintf(stderr, "[%d/%d] %s (%s, tier %d)…\n", index+1, total, s.ID, s.Label, s.Tier)
	}

	results, err := c.Restyle(ctx, *image, styles, *out)
	if err != nil {
		fmt.Fprintf(stderr, "promo-restyle: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output{Source: *image, Results: results}); err != nil {
		fmt.Fprintf(stderr, "promo-restyle: %v\n", err)
		return 1
	}
	return 0
}

// chooseStyles is the forced list when -styles is given, a weighted random
// draw otherwise. A seed makes the draw repeatable.
func chooseStyles(ids string, n int, seed uint64) ([]restyle.Style, error) {
	if strings.TrimSpace(ids) == "" {
		if n <= 0 {
			return nil, fmt.Errorf("-n must be positive")
		}
		if seed != 0 {
			return restyle.Pick(n, restyle.NewRand(seed)), nil
		}
		return restyle.Pick(n, nil), nil
	}
	var out []restyle.Style
	seen := map[string]bool{}
	for _, id := range strings.Split(ids, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		s, ok := restyle.StyleByID(id)
		if !ok {
			return nil, fmt.Errorf("unknown style %q", id)
		}
		seen[id] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-styles listed no style")
	}
	return out, nil
}

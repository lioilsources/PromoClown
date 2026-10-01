// Command promo-tribute drains the tribute queue: for every picture waiting in
// promo-api it renders four restyled variants on ComfyUI and leaves a draft
// behind. It runs on the Spark before midnight, while ComfyUI still has the
// GPU; the draft it makes is approved by a human in Telegram like any other.
//
// Environment: PROMO_API_URL, PROMO_TOKEN, optional COMFY_URL.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/lioilsources/promoclown/internal/client"
	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/restyle"
)

const usage = `usage: promo-tribute run [--variants 4] [--max 0] [--template "..."] [--dry-run]

Renders every queued tribute and drafts the post. Nothing is published.

  --variants N   images per post (default 4, the most X and Bluesky take)
  --max N        stop after N tributes (default 0, the whole queue)
  --template T   post text; {credit} is the author's handle, {styles} the
                 style labels, {by} the bot's name (default TsumikiBot),
                 {link} the project's website (left out by default: the link
                 belongs in the account profile)
  --by NAME      signature under the post (default TsumikiBot)
  --seed N       repeat a run's style choice and generation seeds
  --dry-run      list the queued tributes and the styles each would get;
                 read-only: nothing is claimed, rendered or marked failed,
                 the queue is left exactly as it was

Env: PROMO_API_URL, PROMO_TOKEN, COMFY_URL (default http://127.0.0.1:8188)
`

// The credit stays first — the post exists to point at the artist — with the
// bot's name as a signature underneath, so a viewer who likes the picture
// knows what made it before they check the account's profile. The link
// itself lives in the profile, not in every post — X charges $0.20 for a
// post carrying a link against $0.015 for one without, and three tributes a
// day is the difference between $18 and $1.40 a month. {link} is still
// available for a project that wants it in the text instead of {by}.
const defaultTemplate = "Tribute to {credit} 🎨\n\n{styles}\n\nby {by}"

// defaultBy is the signature {by} expands to when the project does not name
// one of its own.
const defaultBy = "TsumikiBot"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("promo-tribute run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	variants := fs.Int("variants", 4, "")
	max := fs.Int("max", 0, "")
	template := fs.String("template", defaultTemplate, "")
	by := fs.String("by", defaultBy, "")
	seed := fs.Uint64("seed", 0, "")
	comfyURL := fs.String("comfy", envOr("COMFY_URL", "http://127.0.0.1:8188"), "")
	dryRun := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *variants < 1 || *variants > 4 {
		fmt.Fprintln(stderr, "promo-tribute: --variants must be between 1 and 4")
		return 2
	}

	token := os.Getenv("PROMO_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, "promo-tribute: PROMO_TOKEN is not set")
		return 2
	}
	api := client.New(envOr("PROMO_API_URL", "http://192.168.88.88:8094"), token)

	w := &worker{
		api:      api,
		comfy:    restyle.New(*comfyURL),
		variants: *variants,
		template: *template,
		by:       *by,
		out:      stdout,
		errOut:   stderr,
	}
	if *seed != 0 {
		w.comfy.Rand = restyle.NewRand(*seed)
		w.rand = restyle.NewRand(*seed)
	}
	w.comfy.Progress = func(i, total int, s restyle.Style, done bool) {
		state := "rendering"
		if done {
			state = "done"
		}
		fmt.Fprintf(stderr, "  [%d/%d] %s %s\n", i+1, total, s.Label, state)
	}

	if *dryRun {
		n, err := w.preview(ctx, *max)
		if err != nil {
			fmt.Fprintf(stderr, "promo-tribute: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "tributes previewed: %d (dry run, queue untouched)\n", n)
		return 0
	}

	done, failed := w.drain(ctx, *max)
	fmt.Fprintf(stderr, "tributes drafted: %d, failed: %d\n", done, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

type worker struct {
	api      *client.Client
	comfy    *restyle.Client
	variants int
	template string
	by       string
	rand     *rand.Rand
	out      io.Writer
	errOut   io.Writer
}

// previewListLimit is the most tributes a dry run looks at; promo-api caps a
// list at this many anyway.
const previewListLimit = 500

// preview is the dry run. It reads the queue through the list endpoint and
// never claims or finishes anything: claiming flips a row to "running", and a
// row given back with an error lands in "failed", which the agent token cannot
// undo (retry is admin-only). On 2026-09-30 that turned tributes #34–#36 into
// failures.
func (w *worker) preview(ctx context.Context, max int) (int, error) {
	queued, err := w.api.Tributes(ctx, "queued", previewListLimit)
	if err != nil {
		return 0, fmt.Errorf("list queued tributes: %w", err)
	}
	// The list is newest first; a real run claims the oldest first.
	slices.SortFunc(queued, func(a, b model.Tribute) int { return cmp.Compare(a.ID, b.ID) })
	if max > 0 && len(queued) > max {
		queued = queued[:max]
	}
	for _, t := range queued {
		styles := restyle.Pick(w.variants, w.rand)
		ids := make([]string, len(styles))
		for i, s := range styles {
			ids[i] = s.ID
		}
		fmt.Fprintf(w.out, "#%d %s → %s\n", t.ID, t.Credit, strings.Join(ids, ", "))
	}
	return len(queued), nil
}

func (w *worker) drain(ctx context.Context, max int) (done, failed int) {
	for max == 0 || done+failed < max {
		if ctx.Err() != nil {
			return done, failed
		}
		t, err := w.api.ClaimTribute(ctx)
		if client.IsNotFound(err) {
			return done, failed
		}
		if err != nil {
			fmt.Fprintf(w.errOut, "claim failed: %v\n", err)
			return done, failed + 1
		}
		fmt.Fprintf(w.errOut, "tribute #%d: %s by %s\n", t.ID, t.SourcePath, t.Credit)
		if err := w.process(ctx, t); err != nil {
			failed++
			fmt.Fprintf(w.errOut, "tribute #%d failed: %v\n", t.ID, err)
			// The queue must not keep a row "running" after the process that
			// claimed it has given up on it.
			if _, ferr := w.api.FinishTribute(context.WithoutCancel(ctx), t.ID,
				model.TributeDoneRequest{Error: err.Error()}); ferr != nil {
				fmt.Fprintf(w.errOut, "tribute #%d: could not record the failure: %v\n", t.ID, ferr)
			}
			continue
		}
		done++
	}
	return done, failed
}

func (w *worker) process(ctx context.Context, t model.Tribute) error {
	styles := restyle.Pick(w.variants, w.rand)
	labels := make([]string, len(styles))
	ids := make([]string, len(styles))
	for i, s := range styles {
		labels[i], ids[i] = s.Label, s.ID
	}

	dir, err := os.MkdirTemp("", "promo-tribute-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "source"+filepath.Ext(t.SourcePath))
	if err := w.api.DownloadAsset(ctx, t.SourcePath, src); err != nil {
		return fmt.Errorf("fetch %s: %w", t.SourcePath, err)
	}

	results, err := w.comfy.Restyle(ctx, src, styles, filepath.Join(dir, "out"))
	if err != nil {
		return err
	}

	media := make([]string, 0, len(results))
	for _, r := range results {
		asset, err := w.api.UploadAsset(ctx, t.Project, r.Path)
		if err != nil {
			return fmt.Errorf("upload %s: %w", filepath.Base(r.Path), err)
		}
		media = append(media, asset.Path)
	}

	project, err := w.api.Project(ctx, t.Project)
	if err != nil {
		return err
	}
	draft, err := w.api.Draft(ctx, model.DraftRequest{
		Project:    t.Project,
		Platform:   model.PlatformX,
		Text:       postText(w.template, t, project, labels, w.by),
		MediaPaths: media,
	})
	if err != nil {
		return err
	}
	if _, err := w.api.FinishTribute(ctx, t.ID, model.TributeDoneRequest{
		PostID: draft.Post.ID, Styles: ids,
	}); err != nil {
		return fmt.Errorf("draft #%d made, but the queue still says running: %w", draft.Post.ID, err)
	}
	fmt.Fprintf(w.out, "#%d → draft #%d (%s)\n", t.ID, draft.Post.ID, strings.Join(ids, ", "))
	for _, warn := range draft.Warnings {
		fmt.Fprintf(w.errOut, "  warning: %s\n", warn)
	}
	return nil
}

// postText fills the template and, if the result does not fit X, drops the
// style list — the credit and the link are the parts that have to survive.
func postText(template string, t model.Tribute, p model.Project, labels []string, by string) string {
	link := p.WebsiteURL
	if link == "" && len(p.Links()) > 0 {
		link = p.Links()[0]
	}
	fill := func(styles string) string {
		out := strings.NewReplacer(
			"{credit}", t.Credit,
			"{link}", link,
			"{by}", by,
			"{styles}", styles,
			"{note}", t.Note,
		).Replace(template)
		return strings.TrimSpace(collapseBlankLines(out))
	}
	text := fill(strings.Join(labels, " · "))
	if core.XLength(text) > 280 {
		text = fill("")
	}
	return text
}

// collapseBlankLines tidies up after an empty placeholder.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

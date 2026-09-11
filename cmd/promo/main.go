// Command promo is the OpenClaw skill CLI over promo-api. On the Spark it
// holds the agent token: it can read, draft and feed the inbox. With the
// admin token (on the Mac) the approve/reject/edit commands work too.
//
// Environment: PROMO_API_URL, PROMO_TOKEN, optional PROMO_ACTOR.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lioilsources/promoclown/internal/client"
	"github.com/lioilsources/promoclown/internal/model"
)

var version = "dev"

const usage = `promo — promo state for the OpenClaw agent

  promo projects list [--status active] [--md] [--json]
  promo projects show <slug> [--json]
  promo projects assets <slug> [--json]
  promo projects import <projects.yaml>                      (admin token)

  promo posts draft --project <slug> --platform bluesky|x|youtube|reddit --text "..."
                    [--title "..."] [--kind post|short|video|reply] [--media <asset path or local file>]
                    [--reply-to <reddit url>]
  promo posts list [--status draft|approved|scheduled|published|rejected|failed] [--project] [--platform] [--limit]
  promo posts show <id>
  promo posts log [--days 30]
  promo posts approve <id> [--at 2026-09-14T10:00]           (admin token)
  promo posts reject <id> [--reason "..."]                   (admin token)
  promo posts edit <id> [--text] [--title] [--media]         (admin token)
  promo posts retry <id> [--at ...]                          (admin token)

  promo mentions pending [--platform] [--limit]
  promo mentions new                  unreported mentions, marked reported on read
  promo mentions import [file]        JSON array from a monitor CLI (stdin by default)
  promo mentions add --platform --external-id --url --author --text [--kind] [--project]
  promo mentions ack <id>...          mark handled

  promo reviews pending | new | import [file] | ack <id>...

  promo digest [--since 24h] [--silent-if-empty]

Every read command takes --json. Exit status: 0 ok, 1 error, 2 refused by the rules.`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type app struct {
	in       io.Reader
	out, err io.Writer
	api      *client.Client
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	a := &app{in: stdin, out: stdout, err: stderr}
	token := os.Getenv("PROMO_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, "promo: PROMO_TOKEN is not set")
		return 1
	}
	a.api = client.New(envOr("PROMO_API_URL", "http://192.168.88.88:8094"), token)
	a.api.Actor = envOr("PROMO_ACTOR", "cli")

	var err error
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	rest := []string{}
	if len(args) > 2 {
		rest = args[2:]
	}
	switch args[0] {
	case "projects":
		err = a.projects(ctx, sub, rest)
	case "posts":
		err = a.posts(ctx, sub, rest)
	case "mentions":
		err = a.mentions(ctx, sub, rest)
	case "reviews":
		err = a.reviews(ctx, sub, rest)
	case "digest":
		err = a.digest(ctx, args[1:])
	default:
		err = usageError("unknown command %q", args[0])
	}
	return a.exit(err)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type usageErr struct{ msg string }

func (e usageErr) Error() string { return e.msg }

func usageError(format string, args ...any) error { return usageErr{fmt.Sprintf(format, args...)} }

func (a *app) exit(err error) int {
	if err == nil {
		return 0
	}
	var apiErr *client.APIError
	var uerr usageErr
	switch {
	case errors.As(err, &uerr):
		fmt.Fprintf(a.err, "promo: %s\n\n%s\n", uerr.msg, usage)
		return 1
	case errors.As(err, &apiErr) && apiErr.Status == 422:
		fmt.Fprintln(a.err, "promo: refused by the promo rules:")
		for _, p := range apiErr.Problems {
			fmt.Fprintf(a.err, "  - %s\n", p)
		}
		fmt.Fprintln(a.err, "Fix every problem above and draft again.")
		return 2
	case errors.As(err, &apiErr) && apiErr.Status == 403:
		fmt.Fprintf(a.err, "promo: not allowed: %s\n", apiErr.Message)
		return 2
	}
	fmt.Fprintf(a.err, "promo: %v\n", err)
	return 1
}

// parseFlags lets flags and positional arguments mix in any order, so
// "posts show 12 --json" works as well as "posts show --json 12".
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, usageError("unknown flag %s", arg)
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 >= len(args) {
			return nil, usageError("flag %s needs a value", arg)
		}
		i++
		flags = append(flags, args[i])
	}
	if err := fs.Parse(flags); err != nil {
		return nil, usageError("%v", err)
	}
	return positional, nil
}

func parseID(args []string, cmd string) (int64, error) {
	if len(args) != 1 {
		return 0, usageError("%s needs exactly one post id", cmd)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, usageError("%q is not a post id", args[0])
	}
	return id, nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Projects ---------------------------------------------------------------------

func (a *app) projects(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet("projects "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	asMD := fs.Bool("md", false, "")
	status := fs.String("status", "", "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		list, err := a.api.Projects(ctx, *status)
		if err != nil {
			return err
		}
		switch {
		case *asJSON:
			return a.printJSON(list)
		case *asMD:
			fmt.Fprint(a.out, projectsMarkdown(list, time.Now()))
			return nil
		}
		if len(list) == 0 {
			fmt.Fprintln(a.out, "No projects.")
		}
		for _, p := range list {
			fmt.Fprintf(a.out, "%s — %s [%s]\n", p.Slug, p.Name, p.Status)
			if p.Tagline != "" {
				fmt.Fprintf(a.out, "  %s\n", p.Tagline)
			}
			if links := p.Links(); len(links) > 0 {
				fmt.Fprintf(a.out, "  %s\n", strings.Join(links, "  "))
			}
		}
		return nil

	case "show":
		if len(pos) != 1 {
			return usageError("projects show needs a slug")
		}
		p, err := a.api.Project(ctx, pos[0])
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(p)
		}
		fmt.Fprint(a.out, projectSection(p))
		return nil

	case "assets":
		if len(pos) != 1 {
			return usageError("projects assets needs a slug")
		}
		list, err := a.api.Assets(ctx, pos[0])
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(list)
		}
		if len(list) == 0 {
			fmt.Fprintf(a.out, "No assets for %s yet.\n", pos[0])
		}
		for _, as := range list {
			fmt.Fprintf(a.out, "%-6s %8s  %s\n", as.Kind, humanSize(as.Size), as.Path)
		}
		return nil

	case "import":
		if len(pos) != 1 {
			return usageError("projects import needs a YAML or JSON file")
		}
		data, err := os.ReadFile(pos[0])
		if err != nil {
			return err
		}
		var file struct {
			Projects []model.Project `yaml:"projects"`
		}
		if err := yaml.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("%s: %w", pos[0], err)
		}
		saved, err := a.api.UpsertProjects(ctx, file.Projects)
		if err != nil {
			return err
		}
		for _, p := range saved {
			fmt.Fprintf(a.out, "saved %s (%s)\n", p.Slug, p.Status)
		}
		return nil
	}
	return usageError("projects: unknown subcommand %q", sub)
}

func projectSection(p model.Project) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s (`%s`)\n\n", p.Name, p.Slug)
	if p.Status != "active" {
		fmt.Fprintf(&sb, "**Status: %s — do not promote.**\n\n", p.Status)
	}
	if p.Tagline != "" {
		fmt.Fprintf(&sb, "%s\n\n", p.Tagline)
	}
	line := func(label, v string) {
		if v != "" {
			fmt.Fprintf(&sb, "- %s: %s\n", label, v)
		}
	}
	line("App Store", p.StoreIOSURL)
	line("Google Play", p.StoreAndroidURL)
	line("Web", p.WebsiteURL)
	line("Audience", p.Audience)
	line("Tags", strings.Join(p.Tags, ", "))
	line("Assets", "`promo projects assets "+p.Slug+"`")
	if len(p.Hooks) > 0 {
		sb.WriteString("\nHooks:\n")
		for i, h := range p.Hooks {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, h)
		}
	}
	if len(p.ForbiddenClaims) > 0 {
		sb.WriteString("\nNever claim or promise:\n")
		for _, c := range p.ForbiddenClaims {
			fmt.Fprintf(&sb, "- %s\n", c)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// projectsMarkdown renders PROJECTS.md for the agent workspace.
func projectsMarkdown(list []model.Project, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("# PROJECTS.md\n\n")
	fmt.Fprintf(&sb, "Generated from promo-api at %s by `promo projects list --md`. Do not edit by hand;\n", now.UTC().Format(time.RFC3339))
	sb.WriteString("it is overwritten every night. Only `active` projects may be promoted.\n\n")
	if len(list) == 0 {
		sb.WriteString("No projects yet.\n")
	}
	for _, p := range list {
		sb.WriteString(projectSection(p))
	}
	return sb.String()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Posts ------------------------------------------------------------------------

func (a *app) posts(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet("posts "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	project := fs.String("project", "", "")
	platform := fs.String("platform", "", "")
	status := fs.String("status", "", "")
	limit := fs.Int("limit", 50, "")
	days := fs.Int("days", 30, "")
	text := fs.String("text", "", "")
	title := fs.String("title", "", "")
	kind := fs.String("kind", "", "")
	media := fs.String("media", "", "")
	replyTo := fs.String("reply-to", "", "")
	at := fs.String("at", "", "")
	reason := fs.String("reason", "", "")
	revision := fs.Int64("revision", 0, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	switch sub {
	case "draft":
		if *project == "" || *platform == "" || *text == "" {
			return usageError("posts draft needs --project, --platform and --text")
		}
		mediaPath, err := a.resolveMedia(ctx, *project, *media)
		if err != nil {
			return err
		}
		res, err := a.api.Draft(ctx, model.DraftRequest{
			Project: *project, Platform: *platform, Kind: *kind, Title: *title,
			Text: *text, MediaPath: mediaPath, ReplyToURL: *replyTo,
		})
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(res)
		}
		fmt.Fprintf(a.out, "Draft #%d saved for %s on %s.\n", res.Post.ID, res.Post.Project, res.Post.Platform)
		for _, w := range res.Warnings {
			fmt.Fprintf(a.out, "  warning: %s\n", w)
		}
		fmt.Fprintln(a.out, "A preview goes to the human in the Telegram approval bot. Nothing is published until they approve it there.")
		return nil

	case "list":
		list, err := a.api.Posts(ctx, client.PostQuery{Status: *status, Project: *project, Platform: *platform, Limit: *limit})
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(list)
		}
		if len(list) == 0 {
			fmt.Fprintln(a.out, "No posts.")
		}
		for _, p := range list {
			fmt.Fprintf(a.out, "#%-4d %-9s %-7s %-14s %s  %s\n", p.ID, p.Status, p.Platform, p.Project,
				shortTime(firstNonEmpty(p.PublishedAt, p.ScheduledAt, p.CreatedAt)), oneLine(p.Text, 70))
		}
		return nil

	case "show":
		id, err := parseID(pos, "posts show")
		if err != nil {
			return err
		}
		p, err := a.api.Post(ctx, id)
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(p)
		}
		a.printPost(p)
		return nil

	case "log":
		list, err := a.api.PostLog(ctx, *days)
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(list)
		}
		if len(list) == 0 {
			fmt.Fprintf(a.out, "Nothing published or scheduled in the last %d days.\n", *days)
		}
		for _, p := range list {
			fmt.Fprintf(a.out, "%s  %-9s %-7s %-14s #%d\n", shortTime(firstNonEmpty(p.PublishedAt, p.ScheduledAt, p.ApprovedAt)),
				p.Status, p.Platform, p.Project, p.ID)
			if p.Title != "" {
				fmt.Fprintf(a.out, "    title: %s\n", p.Title)
			}
			fmt.Fprintf(a.out, "    %s\n", strings.ReplaceAll(p.Text, "\n", "\n    "))
			if p.ReleaseURL != "" {
				fmt.Fprintf(a.out, "    %s\n", p.ReleaseURL)
			}
		}
		return nil

	case "approve":
		id, err := parseID(pos, "posts approve")
		if err != nil {
			return err
		}
		rev := *revision
		if rev == 0 {
			p, err := a.api.Post(ctx, id)
			if err != nil {
				return err
			}
			rev = p.Revision
		}
		res, err := a.api.Approve(ctx, id, model.ApproveRequest{Revision: rev, ScheduledAt: *at})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "#%d approved, slot %s\n", id, firstNonEmpty(res.Post.ScheduledAt, "manual (reddit)"))
		for _, w := range res.Warnings {
			fmt.Fprintf(a.out, "  warning: %s\n", w)
		}
		return nil

	case "reject":
		id, err := parseID(pos, "posts reject")
		if err != nil {
			return err
		}
		if _, err := a.api.Reject(ctx, id, model.RejectRequest{Reason: *reason}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "#%d rejected\n", id)
		return nil

	case "edit":
		id, err := parseID(pos, "posts edit")
		if err != nil {
			return err
		}
		req := model.EditRequest{}
		if set["text"] {
			req.Text = text
		}
		if set["title"] {
			req.Title = title
		}
		if set["media"] {
			p, err := a.api.Post(ctx, id)
			if err != nil {
				return err
			}
			resolved, err := a.resolveMedia(ctx, p.Project, *media)
			if err != nil {
				return err
			}
			req.MediaPath = &resolved
		}
		res, err := a.api.Edit(ctx, id, req)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "#%d edited, now revision %d\n", id, res.Post.Revision)
		return nil

	case "retry":
		id, err := parseID(pos, "posts retry")
		if err != nil {
			return err
		}
		res, err := a.api.Retry(ctx, id, model.RetryRequest{ScheduledAt: *at})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "#%d queued again, slot %s\n", id, res.Post.ScheduledAt)
		return nil

	case "publish":
		return usageErr{"there is no publish command: posts are published only after a human approves them in the Telegram approval bot"}
	}
	return usageError("posts: unknown subcommand %q", sub)
}

// resolveMedia uploads a local file and returns its media path; anything that
// is not a local file is taken as a path inside the project assets.
func (a *app) resolveMedia(ctx context.Context, project, media string) (string, error) {
	if media == "" {
		return "", nil
	}
	if fi, err := os.Stat(media); err == nil && !fi.IsDir() {
		asset, err := a.api.UploadAsset(ctx, project, media)
		if err != nil {
			return "", fmt.Errorf("upload %s: %w", filepath.Base(media), err)
		}
		return asset.Path, nil
	}
	return media, nil
}

func (a *app) printPost(p model.Post) {
	fmt.Fprintf(a.out, "#%d %s · %s · %s · %s · revision %d\n", p.ID, p.Status, p.Platform, p.Kind, p.Project, p.Revision)
	if p.Title != "" {
		fmt.Fprintf(a.out, "title: %s\n", p.Title)
	}
	if p.ReplyToURL != "" {
		fmt.Fprintf(a.out, "reply to: %s\n", p.ReplyToURL)
	}
	if p.MediaPath != "" {
		fmt.Fprintf(a.out, "media: %s\n", p.MediaPath)
	}
	for _, kv := range [][2]string{{"created", p.CreatedAt}, {"approved", p.ApprovedAt}, {"scheduled", p.ScheduledAt},
		{"published", p.PublishedAt}, {"url", p.ReleaseURL}, {"error", p.Error}} {
		if kv[1] != "" {
			fmt.Fprintf(a.out, "%s: %s\n", kv[0], kv[1])
		}
	}
	fmt.Fprintf(a.out, "\n%s\n", p.Text)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func shortTime(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	if loc, err := time.LoadLocation("Europe/Prague"); err == nil {
		t = t.In(loc)
	}
	return t.Format("2006-01-02 15:04")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Inbox ------------------------------------------------------------------------

func (a *app) readJSONInput(pos []string, v any) error {
	var r io.Reader = a.in
	if len(pos) == 1 && pos[0] != "-" {
		f, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		data = []byte("[]")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("input is not a JSON array: %w", err)
	}
	return nil
}

func parseIDs(args []string) ([]int64, error) {
	if len(args) == 0 {
		return nil, usageError("ack needs at least one id")
	}
	ids := make([]int64, 0, len(args))
	for _, s := range args {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(strings.TrimPrefix(part, "#")); part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return nil, usageError("%q is not an id", part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (a *app) mentions(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet("mentions "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	platform := fs.String("platform", "", "")
	limit := fs.Int("limit", 50, "")
	kind := fs.String("kind", "", "")
	externalID := fs.String("external-id", "", "")
	url := fs.String("url", "", "")
	author := fs.String("author", "", "")
	text := fs.String("text", "", "")
	project := fs.String("project", "", "")
	context_ := fs.String("context", "", "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}

	switch sub {
	case "pending", "new":
		var list []model.Mention
		if sub == "new" {
			list, err = a.api.ClaimMentions(ctx)
		} else {
			f := false
			list, err = a.api.Mentions(ctx, &f, *platform, *limit)
		}
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(list)
		}
		if len(list) == 0 {
			fmt.Fprintln(a.out, "No mentions.")
		}
		for _, m := range list {
			fmt.Fprintf(a.out, "#%d %s %s by %s at %s%s\n", m.ID, m.Platform, m.Kind, firstNonEmpty(m.Author, "?"),
				shortTime(firstNonEmpty(m.PostedAt, m.SeenAt)), projectTag(m.Project))
			if m.Text != "" {
				fmt.Fprintf(a.out, "    %s\n", oneLine(m.Text, 400))
			}
			if m.URL != "" {
				fmt.Fprintf(a.out, "    %s\n", m.URL)
			}
		}
		return nil

	case "import":
		var items []model.Mention
		if err := a.readJSONInput(pos, &items); err != nil {
			return err
		}
		res, err := a.api.ImportMentions(ctx, items)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "mentions: %d received, %d new\n", res.Received, res.Inserted)
		return nil

	case "add":
		if *platform == "" || *externalID == "" {
			return usageError("mentions add needs --platform and --external-id")
		}
		res, err := a.api.ImportMentions(ctx, []model.Mention{{
			Platform: *platform, Kind: *kind, ExternalID: *externalID, URL: *url,
			Author: *author, Text: *text, Project: *project, Context: *context_,
		}})
		if err != nil {
			return err
		}
		if res.Inserted == 0 {
			fmt.Fprintln(a.out, "already known")
		} else {
			fmt.Fprintf(a.out, "saved mention #%d\n", res.IDs[0])
		}
		return nil

	case "ack":
		ids, err := parseIDs(pos)
		if err != nil {
			return err
		}
		res, err := a.api.MentionsHandled(ctx, ids)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "marked %d handled\n", res.Updated)
		return nil
	}
	return usageError("mentions: unknown subcommand %q", sub)
}

func projectTag(slug string) string {
	if slug == "" {
		return ""
	}
	return " [" + slug + "]"
}

func stars(n int) string {
	if n < 0 || n > 5 {
		return strconv.Itoa(n)
	}
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

func (a *app) reviews(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet("reviews "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	limit := fs.Int("limit", 50, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	switch sub {
	case "pending", "new":
		var list []model.Review
		if sub == "new" {
			list, err = a.api.ClaimReviews(ctx)
		} else {
			f := false
			list, err = a.api.Reviews(ctx, &f, *limit)
		}
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(list)
		}
		if len(list) == 0 {
			fmt.Fprintln(a.out, "No reviews.")
		}
		for _, r := range list {
			fmt.Fprintf(a.out, "#%d %s %s %s by %s at %s%s\n", r.ID, r.Store, stars(r.Rating), r.AppID,
				firstNonEmpty(r.Author, "?"), shortTime(firstNonEmpty(r.PostedAt, r.SeenAt)), projectTag(r.Project))
			if r.Title != "" {
				fmt.Fprintf(a.out, "    %s\n", r.Title)
			}
			if r.Text != "" {
				fmt.Fprintf(a.out, "    %s\n", oneLine(r.Text, 600))
			}
		}
		return nil
	case "import":
		var items []model.Review
		if err := a.readJSONInput(pos, &items); err != nil {
			return err
		}
		res, err := a.api.ImportReviews(ctx, items)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "reviews: %d received, %d new\n", res.Received, res.Inserted)
		return nil
	case "ack":
		ids, err := parseIDs(pos)
		if err != nil {
			return err
		}
		res, err := a.api.ReviewsHandled(ctx, ids)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "marked %d handled\n", res.Updated)
		return nil
	}
	return usageError("reviews: unknown subcommand %q", sub)
}

// Digest -----------------------------------------------------------------------

func (a *app) digest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	since := fs.String("since", "24h", "")
	silent := fs.Bool("silent-if-empty", false, "")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	d, err := a.api.Digest(ctx, *since)
	if err != nil {
		return err
	}
	if *silent && d.Empty() {
		return nil
	}
	if *asJSON {
		return a.printJSON(d)
	}
	fmt.Fprintf(a.out, "Digest since %s\n", shortTime(d.Since))
	section := func(title string, n int) bool {
		if n == 0 {
			return false
		}
		fmt.Fprintf(a.out, "\n%s (%d)\n", title, n)
		return true
	}
	if section("Mentions", len(d.Mentions)) {
		for _, m := range d.Mentions {
			fmt.Fprintf(a.out, "- %s %s by %s%s: %s %s\n", m.Platform, m.Kind, firstNonEmpty(m.Author, "?"),
				projectTag(m.Project), oneLine(m.Text, 160), m.URL)
		}
	}
	if section("Reviews", len(d.Reviews)) {
		for _, r := range d.Reviews {
			fmt.Fprintf(a.out, "- %s %s %s%s: %s\n", r.Store, stars(r.Rating), firstNonEmpty(r.Author, "?"),
				projectTag(r.Project), oneLine(firstNonEmpty(r.Title+" — "+r.Text, r.Text), 200))
		}
	}
	if section("Published", len(d.Published)) {
		for _, p := range d.Published {
			fmt.Fprintf(a.out, "- #%d %s %s %s\n", p.ID, p.Platform, p.Project, p.ReleaseURL)
		}
	}
	if section("Failed", len(d.Failed)) {
		for _, p := range d.Failed {
			fmt.Fprintf(a.out, "- #%d %s %s: %s\n", p.ID, p.Platform, p.Project, p.Error)
		}
	}
	if section("Scheduled", len(d.Scheduled)) {
		for _, p := range d.Scheduled {
			fmt.Fprintf(a.out, "- #%d %s %s at %s\n", p.ID, p.Platform, p.Project, shortTime(p.ScheduledAt))
		}
	}
	if section("Waiting for approval", len(d.Drafts)) {
		for _, p := range d.Drafts {
			fmt.Fprintf(a.out, "- #%d %s %s: %s\n", p.ID, p.Platform, p.Project, oneLine(p.Text, 80))
		}
	}
	return nil
}

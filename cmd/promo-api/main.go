// Command promo-api is the stateful half of PromoClown on JODA: the SQLite
// store and HTTP API, the Postiz publisher and the Telegram approval bot.
//
//	promo-api serve          run everything configured in the environment
//	promo-api check          validate config and reach Postiz and Telegram
//	promo-api publish-once   one publisher pass, then exit
//	promo-api sync-once      one Postiz status sync, then exit
//	promo-api backup         consistent SQLite snapshot into backups/, keeps 14
//	promo-api healthcheck    exit 0 if the local /health answers
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // distroless has no zoneinfo

	"github.com/lioilsources/promoclown/internal/api"
	"github.com/lioilsources/promoclown/internal/approvals"
	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/postiz"
	"github.com/lioilsources/promoclown/internal/publisher"
	"github.com/lioilsources/promoclown/internal/telegram"
)

var version = "dev"

type config struct {
	addr, dbPath, assetsDir, dataDir      string
	agentToken, adminToken, webhookSecret string
	schedule                              core.Schedule

	postizURL, postizKey, postizUI string
	integrations                   map[string]string
	publishInterval, syncInterval  time.Duration
	youtubePrivacy, xWhoCanReply   string

	tgToken string
	tgChat  int64
	tgUsers map[int64]bool

	redditRetention time.Duration
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	duration := func(key, def string) time.Duration {
		d, err := time.ParseDuration(env(key, def))
		if err != nil {
			bad("%s: %v", key, err)
		}
		return d
	}

	c := config{
		addr:           env("PROMO_ADDR", ":8094"),
		dataDir:        env("PROMO_DATA_DIR", "/data"),
		agentToken:     os.Getenv("PROMO_AGENT_TOKEN"),
		adminToken:     os.Getenv("PROMO_ADMIN_TOKEN"),
		webhookSecret:  os.Getenv("PROMO_WEBHOOK_SECRET"),
		postizURL:      env("POSTIZ_API_URL", ""),
		postizKey:      os.Getenv("POSTIZ_API_KEY"),
		postizUI:       env("POSTIZ_UI_URL", ""),
		youtubePrivacy: env("PROMO_YOUTUBE_PRIVACY", "public"),
		xWhoCanReply:   env("PROMO_X_WHO_CAN_REPLY", "everyone"),
		tgToken:        os.Getenv("TELEGRAM_APPROVAL_BOT_TOKEN"),
		integrations:   map[string]string{},
		tgUsers:        map[int64]bool{},
	}
	c.dbPath = env("PROMO_DB", filepath.Join(c.dataDir, "promo.db"))
	c.assetsDir = env("PROMO_ASSETS_DIR", filepath.Join(c.dataDir, "assets"))

	// Two different, long tokens: the whole approval model rests on the agent
	// never holding the admin one.
	if len(c.agentToken) < 32 || len(c.adminToken) < 32 {
		bad("PROMO_AGENT_TOKEN and PROMO_ADMIN_TOKEN must both be set, at least 32 characters (openssl rand -hex 32)")
	} else if c.agentToken == c.adminToken {
		bad("PROMO_AGENT_TOKEN and PROMO_ADMIN_TOKEN must differ")
	}

	loc, err := time.LoadLocation(env("PROMO_TZ", "Europe/Prague"))
	if err != nil {
		bad("PROMO_TZ: %v", err)
		loc = time.UTC
	}
	start, end, err := parseWindow(env("PROMO_POST_WINDOW", "09:00-20:00"))
	if err != nil {
		bad("PROMO_POST_WINDOW: %v", err)
	}
	c.schedule = core.Schedule{Loc: loc, WindowStart: start, WindowEnd: end, Lead: duration("PROMO_POST_LEAD", "10m")}

	c.publishInterval = duration("PROMO_PUBLISH_INTERVAL", "5m")
	c.syncInterval = duration("PROMO_SYNC_INTERVAL", "10m")
	c.redditRetention = duration("PROMO_REDDIT_RETENTION", "48h")

	if (c.postizURL == "") != (c.postizKey == "") {
		bad("set both POSTIZ_API_URL and POSTIZ_API_KEY, or neither")
	}
	for _, pair := range splitList(os.Getenv("POSTIZ_INTEGRATIONS")) {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || v == "" {
			bad("POSTIZ_INTEGRATIONS: %q is not platform=id", pair)
			continue
		}
		c.integrations[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}

	if c.tgToken != "" {
		for _, v := range splitList(os.Getenv("TELEGRAM_ALLOWED_USER_IDS")) {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				bad("TELEGRAM_ALLOWED_USER_IDS: %q is not a numeric user id", v)
				continue
			}
			c.tgUsers[id] = true
		}
		if len(c.tgUsers) == 0 {
			bad("TELEGRAM_ALLOWED_USER_IDS is required when TELEGRAM_APPROVAL_BOT_TOKEN is set")
		}
		if v := os.Getenv("TELEGRAM_APPROVAL_CHAT_ID"); v != "" {
			if c.tgChat, err = strconv.ParseInt(v, 10, 64); err != nil {
				bad("TELEGRAM_APPROVAL_CHAT_ID: %v", err)
			}
		} else if len(c.tgUsers) == 1 {
			for id := range c.tgUsers {
				c.tgChat = id // a private chat's id is the user's id
			}
		} else {
			bad("TELEGRAM_APPROVAL_CHAT_ID is required with more than one allowed user")
		}
	}

	if len(problems) > 0 {
		return c, errors.New("configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return c, nil
}

func parseWindow(v string) (time.Duration, time.Duration, error) {
	a, b, ok := strings.Cut(v, "-")
	if !ok {
		return 0, 0, fmt.Errorf("%q is not HH:MM-HH:MM", v)
	}
	parse := func(s string) (time.Duration, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return 0, err
		}
		return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
	}
	start, err := parse(a)
	if err != nil {
		return 0, 0, err
	}
	end, err := parse(b)
	if err != nil {
		return 0, 0, err
	}
	if end <= start {
		return 0, 0, fmt.Errorf("%q: window must end after it starts", v)
	}
	return start, end, nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("PROMO_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	log := newLogger()

	switch cmd {
	case "version":
		fmt.Println(version)
		return
	case "healthcheck":
		os.Exit(healthcheck())
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		err = serve(ctx, cfg, log)
	case "check":
		err = check(ctx, cfg)
	case "publish-once", "sync-once":
		err = once(ctx, cfg, log, cmd)
	case "backup":
		err = backup(ctx, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (serve, check, publish-once, sync-once, backup, healthcheck, version)\n", cmd)
		os.Exit(2)
	}
	if err != nil {
		log.Error(cmd+" failed", "err", err)
		os.Exit(1)
	}
}

// backup snapshots the database into backups/ beside it, keeping 14 copies.
// Safe while serve runs; JODA has no sqlite3 binary to do it from the host.
func backup(ctx context.Context, cfg config) error {
	conn, err := db.Open(cfg.dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	path, err := db.Backup(ctx, conn, filepath.Join(filepath.Dir(cfg.dbPath), "backups"), 14, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func open(cfg config) (*core.Service, func(), error) {
	if err := os.MkdirAll(filepath.Dir(cfg.dbPath), 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(cfg.assetsDir, 0o755); err != nil {
		return nil, nil, err
	}
	conn, err := db.Open(cfg.dbPath)
	if err != nil {
		return nil, nil, err
	}
	svc := core.New(conn, core.Config{AssetsDir: cfg.assetsDir, Schedule: cfg.schedule})
	return svc, func() { conn.Close() }, nil
}

func newPublisher(cfg config, svc *core.Service, log *slog.Logger) *publisher.Publisher {
	if cfg.postizURL == "" {
		return nil
	}
	return publisher.New(svc, postiz.New(cfg.postizURL, cfg.postizKey), publisher.Config{
		Integrations:   cfg.integrations,
		YouTubePrivacy: cfg.youtubePrivacy,
		XWhoCanReply:   cfg.xWhoCanReply,
		Interval:       cfg.publishInterval,
		SyncInterval:   cfg.syncInterval,
	}, log.With("component", "publisher"))
}

func serve(ctx context.Context, cfg config, log *slog.Logger) error {
	svc, closeDB, err := open(cfg)
	if err != nil {
		return err
	}
	defer closeDB()

	pub := newPublisher(cfg, svc, log)
	nudge := func() {}
	if pub != nil {
		go pub.Run(ctx)
		nudge = pub.Nudge
	} else {
		log.Warn("publisher disabled: POSTIZ_API_URL/POSTIZ_API_KEY not set; approved posts will wait")
	}

	if cfg.tgToken != "" {
		bot := approvals.New(svc, telegram.New(cfg.tgToken), approvals.Config{
			ChatID:       cfg.tgChat,
			AllowedUsers: cfg.tgUsers,
			OffsetFile:   filepath.Join(filepath.Dir(cfg.dbPath), "telegram-offset"),
			PostizURL:    cfg.postizUI,
		}, log.With("component", "approvals"), nudge)
		go bot.Run(ctx)
	} else {
		log.Warn("approval bot disabled: TELEGRAM_APPROVAL_BOT_TOKEN not set; nothing can be approved from Telegram")
	}

	go maintain(ctx, svc, cfg, log)

	webhook := func(ctx context.Context, body []byte) {
		if pub != nil {
			pub.HandleWebhook(ctx, body)
		}
	}
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.New(svc, api.Config{AgentToken: cfg.agentToken, AdminToken: cfg.adminToken, WebhookSecret: cfg.webhookSecret}, webhook, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("promo-api listening", "addr", cfg.addr, "version", version, "db", cfg.dbPath,
		"publisher", pub != nil, "approval_bot", cfg.tgToken != "")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// maintain runs housekeeping hourly.
func maintain(ctx context.Context, svc *core.Service, cfg config, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if cfg.redditRetention > 0 {
			if n, err := svc.RedactMentions(ctx, model.PlatformReddit, cfg.redditRetention); err != nil {
				log.Error("reddit retention", "err", err)
			} else if n > 0 {
				log.Info("reddit retention: redacted mentions", "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func once(ctx context.Context, cfg config, log *slog.Logger, cmd string) error {
	svc, closeDB, err := open(cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	pub := newPublisher(cfg, svc, log)
	if pub == nil {
		return errors.New("POSTIZ_API_URL and POSTIZ_API_KEY are required")
	}
	if cmd == "publish-once" {
		return pub.PublishApproved(ctx)
	}
	return pub.Sync(ctx)
}

// check is the deploy smoke test: config parses, the database opens and
// migrates, Postiz answers with its channels, the approval bot token works.
func check(ctx context.Context, cfg config) error {
	w := os.Stdout
	svc, closeDB, err := open(cfg)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer closeDB()
	projects, err := svc.ListProjects(ctx, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "ok   database %s (%d projects)\n", cfg.dbPath, len(projects))

	failed := false
	if cfg.postizURL == "" {
		fmt.Fprintln(w, "skip postiz (not configured)")
	} else {
		pz := postiz.New(cfg.postizURL, cfg.postizKey)
		if list, err := pz.Integrations(ctx); err != nil {
			failed = true
			fmt.Fprintf(w, "FAIL postiz %s: %v\n", cfg.postizURL, err)
		} else {
			fmt.Fprintf(w, "ok   postiz %s\n", cfg.postizURL)
			printIntegrations(w, list)
		}
	}

	if cfg.tgToken == "" {
		fmt.Fprintln(w, "skip telegram approval bot (not configured)")
	} else if me, err := telegram.New(cfg.tgToken).GetMe(ctx); err != nil {
		failed = true
		fmt.Fprintf(w, "FAIL telegram: %v\n", err)
	} else {
		fmt.Fprintf(w, "ok   telegram @%s → chat %d, %d allowed user(s)\n", me.Username, cfg.tgChat, len(cfg.tgUsers))
	}
	if failed {
		return errors.New("check failed")
	}
	return nil
}

func printIntegrations(w io.Writer, list []postiz.Integration) {
	for _, in := range list {
		state := "enabled"
		if in.Disabled {
			state = "disabled"
		}
		fmt.Fprintf(w, "       %-8s %-26s %s (%s)\n", in.Identifier, in.ID, in.Name, state)
	}
}

func healthcheck() int {
	addr := env("PROMO_ADDR", ":8094")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

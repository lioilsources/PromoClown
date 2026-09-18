// Package approvals is the Telegram approval bot. It runs next to promo-api
// on JODA with its own bot token, separate from the OpenClaw agent's bot: a
// Telegram bot can only have one poller, and keeping approvals out of the
// agent's process means no prompt can ever reach them.
package approvals

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/db"
	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/telegram"
)

type Config struct {
	ChatID       int64
	AllowedUsers map[int64]bool
	PollTimeout  time.Duration
	Tick         time.Duration
	OffsetFile   string // remembers the update offset across restarts
	PostizURL    string // for links in notifications
	// TributeProject is the project a picture sent to the bot belongs to
	// unless its caption names another with "#slug".
	TributeProject string
}

type Bot struct {
	svc        *core.Service
	tg         *telegram.Client
	cfg        Config
	log        *slog.Logger
	onApproved func()
}

// New builds the bot. onApproved runs after every approval or retry, so the
// publisher can pick the post up immediately.
func New(svc *core.Service, tg *telegram.Client, cfg Config, log *slog.Logger, onApproved func()) *Bot {
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = 50 * time.Second
	}
	if cfg.Tick == 0 {
		cfg.Tick = 15 * time.Second
	}
	if onApproved == nil {
		onApproved = func() {}
	}
	return &Bot{svc: svc, tg: tg, cfg: cfg, log: log, onApproved: onApproved}
}

func (b *Bot) Run(ctx context.Context) {
	go b.notifyLoop(ctx)
	b.pollLoop(ctx)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Polling ---------------------------------------------------------------------

func (b *Bot) loadOffset() int64 {
	if b.cfg.OffsetFile == "" {
		return 0
	}
	data, err := os.ReadFile(b.cfg.OffsetFile)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n
}

func (b *Bot) saveOffset(offset int64) {
	if b.cfg.OffsetFile == "" {
		return
	}
	if err := os.WriteFile(b.cfg.OffsetFile, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		b.log.Warn("telegram: cannot save offset", "err", err)
	}
}

func (b *Bot) pollLoop(ctx context.Context) {
	offset := b.loadOffset()
	for ctx.Err() == nil {
		updates, err := b.tg.GetUpdates(ctx, offset, int(b.cfg.PollTimeout.Seconds()))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := 5 * time.Second
			var te *telegram.Error
			if errors.As(err, &te) {
				if te.RetryAfter > 0 {
					wait = time.Duration(te.RetryAfter) * time.Second
				}
				if te.Code == 409 {
					b.log.Error("telegram: another process is polling the approval bot token")
					wait = 30 * time.Second
				}
			}
			b.log.Warn("telegram: getUpdates failed", "err", err)
			sleep(ctx, wait)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
		if len(updates) > 0 {
			b.saveOffset(offset)
		}
	}
}

func (b *Bot) allowed(userID int64) bool { return b.cfg.AllowedUsers[userID] }

func (b *Bot) handle(ctx context.Context, u telegram.Update) {
	switch {
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		if !b.allowed(cq.From.ID) {
			b.log.Warn("telegram: button from unknown user", "user", cq.From.ID)
			b.tg.AnswerCallback(ctx, cq.ID, "Nepovoleno")
			return
		}
		cmd, err := ParseCallback(cq.Data)
		if err != nil {
			b.tg.AnswerCallback(ctx, cq.ID, "?")
			return
		}
		res := b.execute(ctx, cmd, cq.From)
		b.tg.AnswerCallback(ctx, cq.ID, res.toast)
		chat := b.cfg.ChatID
		if cq.Message != nil {
			chat = cq.Message.Chat.ID
			if res.clearPreview {
				b.clear(ctx, chat, cq.Message.MessageID)
			}
		}
		b.say(ctx, chat, res.text)

	case u.Message != nil:
		m := u.Message
		if m.From == nil || !b.allowed(m.From.ID) || m.Chat.Type != "private" {
			if m.From != nil {
				b.log.Warn("telegram: message from unknown user or chat", "user", m.From.ID, "chat", m.Chat.ID)
			}
			return
		}
		if len(m.Photo) > 0 || m.Document != nil {
			b.handlePhoto(ctx, m)
			return
		}
		cmd, err := Parse(m.Text)
		if err != nil {
			b.say(ctx, m.Chat.ID, "❓ "+html.EscapeString(err.Error())+"\n\n"+helpText)
			return
		}
		res := b.execute(ctx, cmd, *m.From)
		if res.clearPreview && res.previewMsg != 0 {
			b.clear(ctx, m.Chat.ID, res.previewMsg)
		}
		b.say(ctx, m.Chat.ID, res.text)
	}
}

func (b *Bot) clear(ctx context.Context, chat, msg int64) {
	if err := b.tg.ClearKeyboard(ctx, chat, msg); err != nil {
		b.log.Debug("telegram: clear keyboard", "err", err)
	}
}

func (b *Bot) say(ctx context.Context, chat int64, text string) {
	if text == "" {
		return
	}
	if _, err := b.tg.SendMessage(ctx, chat, text, nil); err != nil {
		b.log.Error("telegram: send failed", "err", err)
	}
}

// Commands --------------------------------------------------------------------

const helpText = `<b>Schvalování postů</b>
<code>ok 12</code> schválit, čas vybere pravidlo frekvence
<code>ok 12 at 2026-09-14T10:00</code> schválit na konkrétní čas
<code>edit 12 nový text</code> přepsat text (přijde nový náhled)
<code>title 12 nový titulek</code> titulek YouTube videa
<code>no 12 důvod</code> zamítnout
<code>retry 12</code> znovu zkusit neúspěšný post
<code>done 12 url</code> Reddit odpověď odeslaná ručně
<code>show 12</code> · <code>list</code>

<b>Tribute</b>
Pošli obrázek a do popisku autora: <code>@artist</code>.
Jiný projekt: <code>#slug</code>. V noci z něj vzniknou 4 obrázky
v různých malířských stylech a přijde návrh ke schválení.`

type result struct {
	text         string
	toast        string
	clearPreview bool
	previewMsg   int64
}

func (b *Bot) execute(ctx context.Context, cmd Command, from telegram.User) result {
	actor := fmt.Sprintf("telegram:%d", from.ID)
	switch cmd.Verb {
	case "help":
		return result{text: helpText}

	case "list":
		drafts, err := b.svc.ListPosts(ctx, core.PostFilter{Status: model.StatusDraft, Limit: 30})
		if err != nil {
			return b.failure(err)
		}
		if len(drafts) == 0 {
			return result{text: "Žádné drafty ke schválení."}
		}
		var sb strings.Builder
		sb.WriteString("<b>Drafty ke schválení</b>\n")
		for _, p := range drafts {
			fmt.Fprintf(&sb, "#%d · %s · %s — %s\n", p.ID, p.Platform, p.Project, html.EscapeString(truncate(firstLine(p.Text), 70)))
		}
		return result{text: sb.String()}

	case "show":
		p, err := b.svc.GetPost(ctx, cmd.ID)
		if err != nil {
			return b.failure(err)
		}
		warns, _ := b.svc.PostWarnings(ctx, p.ID)
		return result{text: fitMessage(previewHTML(p, warns, b.svc.Location()))}

	case "ok":
		p, err := b.svc.GetPost(ctx, cmd.ID)
		if err != nil {
			return b.failure(err)
		}
		rev := cmd.Revision
		if rev == 0 {
			rev = p.NotifiedRevision
		}
		if rev == 0 {
			return result{text: fmt.Sprintf("⏳ #%d ještě nemá náhled. Schvaluj až text, který jsi viděl.", p.ID), toast: "Bez náhledu"}
		}
		res, err := b.svc.Approve(ctx, cmd.ID, model.ApproveRequest{Revision: rev, ScheduledAt: cmd.At, Actor: actor})
		if err != nil {
			return b.failure(err)
		}
		b.onApproved()
		post := res.Post
		var sb strings.Builder
		if post.Platform == model.PlatformReddit {
			fmt.Fprintf(&sb, "✅ #%d schváleno. Reddit se odesílá ručně:\n%s\n\n<pre>%s</pre>\n\nPak pošli <code>done %d odkaz-na-komentář</code>",
				post.ID, html.EscapeString(post.ReplyToURL), html.EscapeString(post.Text), post.ID)
		} else {
			fmt.Fprintf(&sb, "✅ #%d schváleno → <b>%s</b>. Publisher ho během pár minut založí v Postizu.",
				post.ID, localTime(post.ScheduledAt, b.svc.Location()))
		}
		writeWarnings(&sb, res.Warnings)
		return result{text: sb.String(), toast: "Schváleno", clearPreview: true, previewMsg: p.TelegramMsgID}

	case "no":
		p, err := b.svc.Reject(ctx, cmd.ID, model.RejectRequest{Reason: strings.TrimSpace(cmd.Text), Actor: actor})
		if err != nil {
			return b.failure(err)
		}
		return result{text: fmt.Sprintf("🗑 #%d zamítnuto.", p.ID), toast: "Zamítnuto", clearPreview: true, previewMsg: p.TelegramMsgID}

	case "edit", "title":
		req := model.EditRequest{Actor: actor}
		text := strings.TrimSpace(cmd.Text)
		if cmd.Verb == "edit" {
			req.Text = &text
		} else {
			req.Title = &text
		}
		before, err := b.svc.GetPost(ctx, cmd.ID)
		if err != nil {
			return b.failure(err)
		}
		res, err := b.svc.EditDraft(ctx, cmd.ID, req)
		if err != nil {
			return b.failure(err)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "✏️ #%d upraveno (revize %d). Posílám nový náhled ke schválení.", res.Post.ID, res.Post.Revision)
		writeWarnings(&sb, res.Warnings)
		return result{text: sb.String(), clearPreview: true, previewMsg: before.TelegramMsgID}

	case "retry":
		res, err := b.svc.Retry(ctx, cmd.ID, model.RetryRequest{ScheduledAt: cmd.At, Actor: actor})
		if err != nil {
			return b.failure(err)
		}
		b.onApproved()
		var sb strings.Builder
		fmt.Fprintf(&sb, "🔁 #%d znovu ve frontě → <b>%s</b>.", res.Post.ID, localTime(res.Post.ScheduledAt, b.svc.Location()))
		writeWarnings(&sb, res.Warnings)
		return result{text: sb.String()}

	case "done":
		p, err := b.svc.GetPost(ctx, cmd.ID)
		if err != nil {
			return b.failure(err)
		}
		if p.Platform != model.PlatformReddit {
			return result{text: fmt.Sprintf("⛔ done je jen pro Reddit; #%d publikuje Postiz a stav se načte sám.", p.ID)}
		}
		p, err = b.svc.MarkPublished(ctx, cmd.ID, strings.TrimSpace(cmd.Text), time.Time{}, actor)
		if err != nil {
			return b.failure(err)
		}
		return result{text: fmt.Sprintf("🚀 #%d zapsáno jako publikované.", p.ID)}
	}
	return result{text: helpText}
}

func (b *Bot) failure(err error) result {
	var verr *core.ValidationError
	switch {
	case errors.As(err, &verr):
		var sb strings.Builder
		sb.WriteString("⚠️ Nejde to:")
		for _, p := range verr.Problems {
			sb.WriteString("\n• " + html.EscapeString(p))
		}
		return result{text: sb.String(), toast: "Nejde to"}
	case errors.Is(err, core.ErrNotFound):
		return result{text: "🔍 " + html.EscapeString(err.Error()), toast: "Nenalezeno"}
	case errors.Is(err, core.ErrConflict):
		return result{text: "⛔ " + html.EscapeString(err.Error()), toast: "Konflikt"}
	}
	b.log.Error("approval command failed", "err", err)
	return result{text: "💥 Chyba: " + html.EscapeString(err.Error()), toast: "Chyba"}
}

// Notifications -----------------------------------------------------------------

func (b *Bot) notifyLoop(ctx context.Context) {
	ticker := time.NewTicker(b.cfg.Tick)
	defer ticker.Stop()
	for {
		b.notifyDrafts(ctx)
		b.notifyEvents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) notifyDrafts(ctx context.Context) {
	drafts, err := b.svc.DraftsToNotify(ctx)
	if err != nil {
		b.log.Error("drafts to notify", "err", err)
		return
	}
	for _, d := range drafts {
		warns, err := b.svc.PostWarnings(ctx, d.ID)
		if err != nil {
			b.log.Error("post warnings", "post", d.ID, "err", err)
			return
		}
		if d.TelegramMsgID != 0 {
			b.clear(ctx, b.cfg.ChatID, d.TelegramMsgID) // the old preview is stale now
		}
		msgID, err := b.sendPreview(ctx, d, warns)
		if err != nil {
			b.log.Error("telegram: preview failed, retrying next tick", "post", d.ID, "err", err)
			return
		}
		if err := b.svc.SetPostNotified(ctx, d.ID, d.Revision, msgID); err != nil {
			b.log.Error("set notified", "post", d.ID, "err", err)
		}
	}
}

func (b *Bot) keyboard(p model.Post) *telegram.Keyboard {
	return &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{{
		{Text: "✅ Schválit", CallbackData: CallbackData("ok", p.ID, p.Revision)},
		{Text: "❌ Zamítnout", CallbackData: CallbackData("no", p.ID, p.Revision)},
	}}}
}

// sendPreview posts the draft exactly as it would go out, media included, and
// returns the id of the message carrying the buttons.
func (b *Bot) sendPreview(ctx context.Context, p model.Post, warns []string) (int64, error) {
	body := previewHTML(p, warns, b.svc.Location())
	kb := b.keyboard(p)
	chat := b.cfg.ChatID

	if len(p.MediaPaths) > 0 {
		if sent, id, note := b.sendPreviewMedia(ctx, p, body, kb); sent {
			if id != 0 {
				return id, nil
			}
		} else if note != "" {
			body += "\n" + note
		}
	}

	if visibleLen(body) > 4000 {
		// Long YouTube descriptions: full text first, then a short control message.
		for _, chunk := range splitRunes(p.Text, 3500) {
			if _, err := b.tg.SendMessage(ctx, chat, html.EscapeString(chunk), nil); err != nil {
				return 0, err
			}
		}
		body = previewHeader(p, warns, b.svc.Location()) + "\n(celý text výše)"
	}
	msg, err := b.tg.SendMessage(ctx, chat, body, kb)
	return msg.MessageID, err
}

// sendPreviewMedia returns sent=true when the media went out; id is non-zero
// when the buttons went with it. Several images travel as one album, which
// Telegram will not decorate with buttons, so those follow in their own
// message.
func (b *Bot) sendPreviewMedia(ctx context.Context, p model.Post, body string, kb *telegram.Keyboard) (sent bool, id int64, note string) {
	files, notes := b.openMedia(p)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	switch {
	case len(files) == 0:
		return false, 0, strings.Join(notes, "\n")
	case len(files) > 1:
		items := make([]telegram.GroupItem, len(files))
		for i, f := range files {
			items[i] = telegram.GroupItem{Kind: f.kind, Filename: filepath.Base(f.Name()), Content: f}
		}
		caption := ""
		if visibleLen(body) <= 1024 && len(notes) == 0 {
			caption = body
		}
		if _, err := b.tg.SendMediaGroup(ctx, b.cfg.ChatID, items, caption); err != nil {
			b.log.Warn("telegram: album preview failed, sending text", "post", p.ID, "err", err)
			return false, 0, mediaList(p.MediaPaths)
		}
		if caption != "" {
			// The album carries the text; the buttons still need a message.
			msg, err := b.tg.SendMessage(ctx, b.cfg.ChatID, previewHeader(p, nil, b.svc.Location()), kb)
			if err != nil {
				return true, 0, ""
			}
			return true, msg.MessageID, ""
		}
		return true, 0, strings.Join(notes, "\n")
	}

	f := files[0]
	if visibleLen(body) <= 1024 && len(notes) == 0 {
		msg, err := b.tg.SendMedia(ctx, b.cfg.ChatID, f.kind, filepath.Base(f.Name()), f, body, kb)
		if err == nil {
			return true, msg.MessageID, ""
		}
		b.log.Warn("telegram: media preview failed, sending text", "post", p.ID, "err", err)
		return false, 0, mediaList(p.MediaPaths)
	}
	if _, err := b.tg.SendMedia(ctx, b.cfg.ChatID, f.kind, filepath.Base(f.Name()), f, "", nil); err != nil {
		b.log.Warn("telegram: media preview failed", "post", p.ID, "err", err)
		return false, 0, mediaList(p.MediaPaths)
	}
	return true, 0, strings.Join(notes, "\n")
}

// mediaFile is an open asset plus what Telegram should send it as.
type mediaFile struct {
	*os.File
	kind string
}

// openMedia opens what can be previewed and describes what cannot, so a post
// with one oversized image still shows the other three.
func (b *Bot) openMedia(p model.Post) (files []*mediaFile, notes []string) {
	for _, rel := range p.MediaPaths {
		path, err := b.svc.AssetPath(rel)
		if err != nil {
			notes = append(notes, "📎 "+html.EscapeString(err.Error()))
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			notes = append(notes, "📎 médium nenalezeno: "+html.EscapeString(rel))
			continue
		}
		kind := core.MediaKind(path)
		limit := int64(10 << 20)
		if kind == "video" {
			limit = 50 << 20
		}
		if kind == "other" || fi.Size() > limit {
			notes = append(notes, fmt.Sprintf("📎 %s (%d MB, na náhled moc velké)", html.EscapeString(rel), fi.Size()>>20))
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			notes = append(notes, "📎 "+html.EscapeString(err.Error()))
			continue
		}
		files = append(files, &mediaFile{File: f, kind: kind})
	}
	return files, notes
}

func mediaList(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = "📎 " + html.EscapeString(p)
	}
	return strings.Join(out, "\n")
}

func (b *Bot) notifyEvents(ctx context.Context) {
	events, err := b.svc.UnnotifiedEvents(ctx)
	if err != nil {
		b.log.Error("unnotified events", "err", err)
		return
	}
	for _, e := range events {
		p, err := b.svc.GetPost(ctx, e.PostID)
		if err != nil {
			b.log.Error("event post", "event", e.ID, "err", err)
			continue
		}
		if _, err := b.tg.SendMessage(ctx, b.cfg.ChatID, eventHTML(e, p, b.svc.Location(), b.cfg.PostizURL), nil); err != nil {
			b.log.Error("telegram: event notification failed, retrying next tick", "event", e.ID, "err", err)
			return
		}
		if err := b.svc.MarkEventNotified(ctx, e.ID); err != nil {
			b.log.Error("mark event notified", "event", e.ID, "err", err)
		}
	}
}

// Formatting ----------------------------------------------------------------------

func previewHeader(p model.Post, warns []string, loc *time.Location) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "📝 <b>#%d</b> · %s · %s · rev %d", p.ID, p.Platform, html.EscapeString(p.Project), p.Revision)
	if p.Status != model.StatusDraft {
		fmt.Fprintf(&sb, " · <b>%s</b>", p.Status)
		if p.ScheduledAt != "" {
			sb.WriteString(" " + localTime(p.ScheduledAt, loc))
		}
	}
	if p.Title != "" {
		sb.WriteString("\n<b>" + html.EscapeString(p.Title) + "</b>")
	}
	if p.ReplyToURL != "" {
		sb.WriteString("\n↪️ odpověď na " + html.EscapeString(p.ReplyToURL))
	}
	writeWarnings(&sb, warns)
	if p.Status == model.StatusDraft {
		fmt.Fprintf(&sb, "\n<code>ok %d</code> · <code>ok %d at 2026-09-14T10:00</code> · <code>edit %d …</code> · <code>no %d</code>", p.ID, p.ID, p.ID, p.ID)
	}
	return sb.String()
}

func previewHTML(p model.Post, warns []string, loc *time.Location) string {
	return previewHeader(p, warns, loc) + "\n\n" + html.EscapeString(p.Text)
}

func eventHTML(e model.PostEvent, p model.Post, loc *time.Location, postizURL string) string {
	head := fmt.Sprintf("#%d · %s · %s", p.ID, p.Platform, html.EscapeString(p.Project))
	switch e.Action {
	case "scheduled":
		s := fmt.Sprintf("📅 %s naplánováno v Postizu na <b>%s</b>", head, localTime(e.Detail, loc))
		if postizURL != "" {
			s += "\n" + html.EscapeString(postizURL)
		}
		return s
	case "published":
		s := "🚀 " + head + " publikováno"
		if p.ReleaseURL != "" {
			s += "\n" + html.EscapeString(p.ReleaseURL)
		}
		return s
	case "failed":
		return fmt.Sprintf("❌ %s selhalo: %s\n<code>retry %d</code> zkusí znovu", head, html.EscapeString(e.Detail), p.ID)
	}
	return fmt.Sprintf("ℹ️ %s %s %s", head, e.Action, html.EscapeString(e.Detail))
}

func writeWarnings(sb *strings.Builder, warns []string) {
	for _, w := range warns {
		sb.WriteString("\n⚠️ " + html.EscapeString(w))
	}
}

var czechDays = [...]string{"ne", "po", "út", "st", "čt", "pá", "so"}

func localTime(stored string, loc *time.Location) string {
	t, err := db.ParseTime(stored)
	if err != nil {
		return html.EscapeString(stored)
	}
	t = t.In(loc)
	return fmt.Sprintf("%s %s", czechDays[t.Weekday()], t.Format("2.1. 15:04"))
}

// visibleLen approximates Telegram's limit: UTF-16 units of the text after
// HTML tags are stripped and entities decoded.
func visibleLen(s string) int {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return len(utf16.Encode([]rune(html.UnescapeString(sb.String()))))
}

func fitMessage(s string) string {
	if visibleLen(s) <= 4000 {
		return s
	}
	return html.EscapeString(truncate(html.UnescapeString(s), 3900))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func splitRunes(s string, n int) []string {
	r := []rune(s)
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}

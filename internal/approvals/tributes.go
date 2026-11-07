package approvals

import (
	"context"
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"github.com/lioilsources/promoclown/internal/core"
	"github.com/lioilsources/promoclown/internal/model"
	"github.com/lioilsources/promoclown/internal/telegram"
)

// A picture sent to the approval bot with an artist's handle in the caption
// joins the tribute queue. The night job restyles it and drafts the post; this
// only stores the file and the handle, so it works while the model is down.
// An album is several pictures under one caption: each becomes its own
// tribute with the same credit (albums.go collects them).

// maxTributeBytes keeps a stray 40 MB screenshot out of the assets directory.
// The restyle graph renders from a bucket around one megapixel anyway.
const maxTributeBytes = 20 << 20

const askCredit = "K obrázku napiš autora, třeba <code>@artist</code>. " +
	"Jiný projekt vybereš <code>#slug</code>."

// handlePhoto queues a picture for the night run. The caption names the artist
// to credit, and optionally the project as "#slug".
func (b *Bot) handlePhoto(ctx context.Context, m *telegram.Message) {
	if _, _, _, ok := pickFile(m); !ok {
		b.say(ctx, m.Chat.ID, "❓ Pošli obrázek (foto nebo soubor png/jpg).")
		return
	}
	project, credit, note := parseCaption(m.Caption, b.cfg.TributeProject)
	if credit == "" {
		b.say(ctx, m.Chat.ID, "❓ "+askCredit)
		return
	}
	t, problem := b.queueTribute(ctx, m, project, credit, note)
	if problem != "" {
		b.say(ctx, m.Chat.ID, "❌ "+problem)
		return
	}
	b.say(ctx, m.Chat.ID, fmt.Sprintf(
		"🎨 Tribute #%d pro %s (%s) je ve frontě, čeká %d. "+
			"V noci z něj vzniknou 4 obrázky a přijde ti návrh ke schválení.",
		t.ID, html.EscapeString(t.Credit), html.EscapeString(t.Project), b.queuedTributes(ctx)))
}

// handleAlbum queues every picture of an album as its own tribute, all under
// the one caption the album carries.
func (b *Bot) handleAlbum(ctx context.Context, msgs []*telegram.Message) {
	chat := msgs[0].Chat.ID
	project, credit, note := parseCaption(albumCaption(msgs), b.cfg.TributeProject)
	if credit == "" {
		b.say(ctx, chat, fmt.Sprintf("❓ Album (%d obrázků) jsem nezařadil. %s Pošli ho znovu s popiskem.",
			len(msgs), askCredit))
		return
	}

	var ids, failed []string
	for i, m := range msgs {
		t, problem := b.queueTribute(ctx, m, project, credit, note)
		if problem != "" {
			failed = append(failed, fmt.Sprintf("%d. obrázek: %s", i+1, problem))
			continue
		}
		ids = append(ids, fmt.Sprintf("#%d", t.ID))
	}
	b.say(ctx, chat, albumReport(ids, failed, credit, project, b.queuedTributes(ctx)))
}

// albumReport is the one reply to an album, HTML for Telegram.
func albumReport(ids, failed []string, credit, project string, queued int64) string {
	var out strings.Builder
	if len(ids) > 0 {
		fmt.Fprintf(&out, "🎨 %d× tribute pro %s (%s) ve frontě: %s, čeká celkem %d. "+
			"V noci z každého vzniknou 4 obrázky a ke každému ti přijde návrh ke schválení.",
			len(ids), html.EscapeString(credit), html.EscapeString(project),
			strings.Join(ids, ", "), queued)
	}
	if len(failed) > 0 {
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString("❌ Nezařazeno:\n" + strings.Join(failed, "\n"))
	}
	return out.String()
}

// queueTribute downloads one picture and queues it. A non-empty problem is
// HTML-escaped text for the owner.
func (b *Bot) queueTribute(ctx context.Context, m *telegram.Message, project, credit, note string) (model.Tribute, string) {
	fileID, filename, size, ok := pickFile(m)
	if !ok {
		return model.Tribute{}, "není to obrázek (foto nebo soubor png/jpg)"
	}
	if size > maxTributeBytes {
		return model.Tribute{}, fmt.Sprintf("obrázek má %d MB, víc než %d MB neberu", size>>20, maxTributeBytes>>20)
	}

	file, err := b.tg.GetFile(ctx, fileID)
	if err != nil {
		b.log.Error("telegram: getFile", "err", err)
		return model.Tribute{}, "Telegram mi ten soubor nedal: " + html.EscapeString(err.Error())
	}
	body, err := b.tg.Download(ctx, file.FilePath)
	if err != nil {
		b.log.Error("telegram: download", "err", err)
		return model.Tribute{}, "stažení selhalo: " + html.EscapeString(err.Error())
	}
	defer body.Close()

	if name := filepath.Base(file.FilePath); filename == "" {
		filename = name
	}
	asset, err := b.svc.SaveUpload(ctx, project, filename, body)
	if err != nil {
		return model.Tribute{}, html.EscapeString(err.Error())
	}
	t, err := b.svc.EnqueueTribute(ctx, model.TributeRequest{
		Project: project, Credit: credit, SourcePath: asset.Path, Note: note,
		Actor: fmt.Sprintf("telegram:%d", m.From.ID),
	})
	if err != nil {
		return model.Tribute{}, html.EscapeString(err.Error())
	}
	return t, ""
}

func (b *Bot) queuedTributes(ctx context.Context) int64 {
	queued, err := b.svc.QueuedTributes(ctx)
	if err != nil {
		b.log.Warn("tribute count", "err", err)
	}
	return queued
}

// pickFile takes the largest photo size Telegram offers, or an image sent as a
// document — which is how a picture arrives uncompressed.
func pickFile(m *telegram.Message) (fileID, filename string, size int64, ok bool) {
	if n := len(m.Photo); n > 0 {
		p := m.Photo[n-1]
		return p.FileID, "", p.FileSize, true
	}
	if d := m.Document; d != nil && core.MediaKind(d.FileName) == "image" {
		return d.FileID, d.FileName, d.FileSize, true
	}
	return "", "", 0, false
}

// parseCaption reads "@artist #project rest of the note" in any order.
func parseCaption(caption, defaultProject string) (project, credit, note string) {
	project = defaultProject
	var rest []string
	for _, word := range strings.Fields(caption) {
		switch {
		case credit == "" && strings.HasPrefix(word, "@") && len(word) > 1:
			credit = core.NormalizeCredit(word)
		case strings.HasPrefix(word, "#") && len(word) > 1:
			project = strings.ToLower(word[1:])
		case credit == "" && strings.Contains(word, "/") && strings.Contains(word, "."):
			credit = core.NormalizeCredit(word) // a pasted profile URL
		default:
			rest = append(rest, word)
		}
	}
	return project, credit, strings.Join(rest, " ")
}

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

// maxTributeBytes keeps a stray 40 MB screenshot out of the assets directory.
// The restyle graph renders from a bucket around one megapixel anyway.
const maxTributeBytes = 20 << 20

// handlePhoto queues a picture for the night run. The caption names the artist
// to credit, and optionally the project as "#slug".
func (b *Bot) handlePhoto(ctx context.Context, m *telegram.Message) {
	fileID, filename, size, ok := pickFile(m)
	if !ok {
		b.say(ctx, m.Chat.ID, "❓ Pošli obrázek (foto nebo soubor png/jpg).")
		return
	}
	if size > maxTributeBytes {
		b.say(ctx, m.Chat.ID, fmt.Sprintf("❌ Obrázek má %d MB, víc než %d MB neberu.", size>>20, maxTributeBytes>>20))
		return
	}

	project, credit, note := parseCaption(m.Caption, b.cfg.TributeProject)
	if credit == "" {
		b.say(ctx, m.Chat.ID, "❓ K obrázku napiš autora, třeba <code>@artist</code>. "+
			"Jiný projekt vybereš <code>#slug</code>.")
		return
	}

	file, err := b.tg.GetFile(ctx, fileID)
	if err != nil {
		b.log.Error("telegram: getFile", "err", err)
		b.say(ctx, m.Chat.ID, "❌ Telegram mi ten soubor nedal: "+html.EscapeString(err.Error()))
		return
	}
	body, err := b.tg.Download(ctx, file.FilePath)
	if err != nil {
		b.log.Error("telegram: download", "err", err)
		b.say(ctx, m.Chat.ID, "❌ Stažení selhalo: "+html.EscapeString(err.Error()))
		return
	}
	defer body.Close()

	if name := filepath.Base(file.FilePath); filename == "" {
		filename = name
	}
	asset, err := b.svc.SaveUpload(ctx, project, filename, body)
	if err != nil {
		b.say(ctx, m.Chat.ID, "❌ "+html.EscapeString(err.Error()))
		return
	}
	t, err := b.svc.EnqueueTribute(ctx, model.TributeRequest{
		Project: project, Credit: credit, SourcePath: asset.Path, Note: note,
		Actor: fmt.Sprintf("telegram:%d", m.From.ID),
	})
	if err != nil {
		b.say(ctx, m.Chat.ID, "❌ "+html.EscapeString(err.Error()))
		return
	}
	queued, err := b.svc.QueuedTributes(ctx)
	if err != nil {
		b.log.Warn("tribute count", "err", err)
	}
	b.say(ctx, m.Chat.ID, fmt.Sprintf(
		"🎨 Tribute #%d pro %s (%s) je ve frontě, čeká %d. "+
			"V noci z něj vzniknou 4 obrázky a přijde ti návrh ke schválení.",
		t.ID, html.EscapeString(t.Credit), html.EscapeString(t.Project), queued))
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

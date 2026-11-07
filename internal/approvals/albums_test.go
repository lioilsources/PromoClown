package approvals

import (
	"strings"
	"testing"
	"time"

	"github.com/lioilsources/promoclown/internal/telegram"
)

func albumMsg(id, chat int64, group, caption string) *telegram.Message {
	return &telegram.Message{
		MessageID: id, Chat: telegram.Chat{ID: chat}, Caption: caption, MediaGroupID: group,
		Photo: []telegram.PhotoSize{{FileID: "f"}},
	}
}

func TestAlbumWaitsUntilQuiet(t *testing.T) {
	var a albumBuffer
	t0 := time.Unix(1000, 0)
	a.add(albumMsg(11, 1, "g", ""), t0)
	a.add(albumMsg(10, 1, "g", "@artist"), t0.Add(500*time.Millisecond))

	if got := a.due(t0.Add(time.Second)); len(got) != 0 || !a.pending() {
		t.Fatalf("album flushed while pictures may still arrive: %v", got)
	}
	// A picture in a later getUpdates batch still joins the same album.
	a.add(albumMsg(12, 1, "g", ""), t0.Add(1200*time.Millisecond))

	got := a.due(t0.Add(1200*time.Millisecond + albumQuiet))
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("want one album of 3, got %v", got)
	}
	for i, want := range []int64{10, 11, 12} {
		if got[0][i].MessageID != want {
			t.Errorf("picture %d is message %d, want %d (send order)", i, got[0][i].MessageID, want)
		}
	}
	if albumCaption(got[0]) != "@artist" {
		t.Errorf("caption %q, want the one picture that has it", albumCaption(got[0]))
	}
	if a.pending() {
		t.Error("flushed album is still pending")
	}
}

func TestAlbumsAreSeparate(t *testing.T) {
	var a albumBuffer
	t0 := time.Unix(1000, 0)
	a.add(albumMsg(20, 1, "b", "@second"), t0)
	a.add(albumMsg(10, 1, "a", "@first"), t0)
	a.add(albumMsg(30, 2, "a", "@other-chat"), t0) // same group id, another chat
	got := a.due(t0.Add(albumQuiet))
	if len(got) != 3 {
		t.Fatalf("want 3 albums, got %d", len(got))
	}
	for i, want := range []string{"@first", "@second", "@other-chat"} {
		if c := albumCaption(got[i]); c != want {
			t.Errorf("album %d caption %q, want %q", i, c, want)
		}
	}
}

func TestAlbumReport(t *testing.T) {
	ok := albumReport([]string{"#4", "#5"}, nil, "@a<b", "tsumiki", 7)
	if !strings.Contains(ok, "2× tribute pro @a&lt;b (tsumiki) ve frontě: #4, #5, čeká celkem 7") {
		t.Errorf("report: %s", ok)
	}
	if strings.Contains(ok, "Nezařazeno") {
		t.Errorf("no failures, but the report lists some: %s", ok)
	}
	mixed := albumReport([]string{"#4"}, []string{"2. obrázek: x"}, "@a", "tsumiki", 1)
	if !strings.Contains(mixed, "#4") || !strings.Contains(mixed, "❌ Nezařazeno:\n2. obrázek: x") {
		t.Errorf("mixed report: %s", mixed)
	}
	if none := albumReport(nil, []string{"1. obrázek: x"}, "@a", "tsumiki", 0); strings.HasPrefix(none, "🎨") || strings.HasPrefix(none, "\n") {
		t.Errorf("all failed, report: %q", none)
	}
}

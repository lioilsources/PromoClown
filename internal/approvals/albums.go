package approvals

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/telegram"
)

// Telegram delivers an album as one message per picture, sharing a
// media_group_id, and only one of them carries the caption — not necessarily
// the first, and not necessarily in the same getUpdates batch. The bot holds
// the pictures of an album until no new one has arrived for albumQuiet, then
// queues them all under the album's caption: one tribute per picture.

const albumQuiet = 1500 * time.Millisecond

type pendingAlbum struct {
	msgs []*telegram.Message
	last time.Time
}

// albumBuffer is touched only by the poll loop, so it needs no lock.
type albumBuffer struct {
	groups map[string]*pendingAlbum
}

func (a *albumBuffer) add(m *telegram.Message, now time.Time) {
	if a.groups == nil {
		a.groups = map[string]*pendingAlbum{}
	}
	key := fmt.Sprintf("%d/%s", m.Chat.ID, m.MediaGroupID)
	g := a.groups[key]
	if g == nil {
		g = &pendingAlbum{}
		a.groups[key] = g
	}
	g.msgs = append(g.msgs, m)
	g.last = now
}

func (a *albumBuffer) pending() bool { return len(a.groups) > 0 }

// due removes and returns the albums that have been quiet for albumQuiet,
// each in the order the pictures were sent, oldest album first.
func (a *albumBuffer) due(now time.Time) [][]*telegram.Message {
	var out [][]*telegram.Message
	for key, g := range a.groups {
		if now.Sub(g.last) < albumQuiet {
			continue
		}
		sort.Slice(g.msgs, func(i, j int) bool { return g.msgs[i].MessageID < g.msgs[j].MessageID })
		out = append(out, g.msgs)
		delete(a.groups, key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0].MessageID < out[j][0].MessageID })
	return out
}

// albumCaption is the caption of whichever picture carries one.
func albumCaption(msgs []*telegram.Message) string {
	for _, m := range msgs {
		if c := strings.TrimSpace(m.Caption); c != "" {
			return c
		}
	}
	return ""
}

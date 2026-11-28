package approvals

import (
	"strings"
	"testing"

	"github.com/lioilsources/promoclown/internal/model"
)

func TestDraftListOldestFirstWithBatchesCollapsed(t *testing.T) {
	// Newest first, as ListPosts returns them.
	var posts []model.Post
	for id := int64(683); id >= 655; id-- {
		posts = append(posts, model.Post{ID: id, Project: "tsumiki", Platform: "x", Text: "Tribute to @someone 🎨\n\nstyles"})
	}
	posts = append(posts,
		model.Post{ID: 143, Project: "kirian", Platform: "bluesky", Text: "Kirian is a roguelike"},
		model.Post{ID: 142, Project: "doggiowars", Platform: "bluesky", Text: "I built DoggioWars"},
	)

	got := draftList(posts)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 3 lines, got %d:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[1], "#142 · bluesky · doggiowars") || !strings.HasPrefix(lines[2], "#143 · bluesky · kirian") {
		t.Errorf("oldest drafts must come first:\n%s", got)
	}
	if !strings.Contains(lines[3], "tsumiki · x — <b>29 draftů</b> #655–#683") {
		t.Errorf("tribute batch not collapsed:\n%s", got)
	}
}

func TestDraftListSmallGroupListed(t *testing.T) {
	posts := []model.Post{
		{ID: 3, Project: "a", Platform: "x", Text: "three"},
		{ID: 2, Project: "a", Platform: "x", Text: "two"},
		{ID: 1, Project: "a", Platform: "x", Text: "one <b>"},
	}
	got := draftList(posts)
	for _, want := range []string{"#1 · x · a — one &lt;b&gt;", "#2 · x · a — two", "#3 · x · a — three"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

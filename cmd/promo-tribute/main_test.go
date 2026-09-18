package main

import (
	"strings"
	"testing"

	"github.com/lioilsources/promoclown/internal/model"
)

func TestPostText(t *testing.T) {
	tribute := model.Tribute{Credit: "@artist"}
	project := model.Project{Slug: "tsumiki", WebsiteURL: "https://t.me/tsumikimanga_bot"}
	labels := []string{"Lempicka (Art Deco)", "Chagall"}

	got := postText(defaultTemplate, tribute, project, labels, "TsumikiBot")
	for _, want := range []string{"Tribute to @artist", "Lempicka (Art Deco) · Chagall", "by TsumikiBot"} {
		if !strings.Contains(got, want) {
			t.Errorf("postText = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "{") {
		t.Errorf("postText = %q, an unfilled placeholder remains", got)
	}

	// A credit long enough to push the styled text past 280 still has to keep
	// the credit and the signature — those are the two things the post exists
	// for — and drop the style list instead.
	longLabels := make([]string, 20)
	for i := range longLabels {
		longLabels[i] = "A Very Long Painter Style Name Indeed"
	}
	got = postText(defaultTemplate, tribute, project, longLabels, "TsumikiBot")
	if !strings.Contains(got, "Tribute to @artist") || !strings.Contains(got, "by TsumikiBot") {
		t.Errorf("postText (long) = %q, credit and signature must survive", got)
	}
	if strings.Contains(got, "A Very Long Painter") {
		t.Errorf("postText (long) = %q, the style list should have been dropped", got)
	}

	// {by} defaults to whatever the caller passes, and is absent if asked to be.
	got = postText("{credit} — {by}", tribute, project, nil, "")
	if got != "@artist —" {
		t.Errorf("postText with empty by = %q, want %q", got, "@artist —")
	}
}

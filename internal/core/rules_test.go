package core

import (
	"strings"
	"testing"
	"time"

	"github.com/lioilsources/promoclown/internal/model"
)

func TestXLength(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"hello", 5},
		{"see https://apps.apple.com/app/id123456789?with=long&query=params", 4 + 23},
		{"čeština", 7},       // Latin Extended weighs 1
		{"日本", 4},            // CJK weighs 2
		{"ship it 🚀", 8 + 2}, // emoji weighs 2
	}
	for _, c := range cases {
		if got := XLength(c.in); got != c.want {
			t.Errorf("XLength(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGraphemeLength(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"hello", 5},
		{"é", 1},    // e + combining acute
		{"👍🏽", 1},    // skin tone modifier
		{"👩‍💻", 1},   // ZWJ sequence
		{"🇨🇿🇺🇸", 2},  // two flags
		{"❤️ ok", 4}, // variation selector
	}
	for _, c := range cases {
		if got := GraphemeLength(c.in); got != c.want {
			t.Errorf("GraphemeLength(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestValidateDraft(t *testing.T) {
	proj := model.Project{
		Slug:            "kiran",
		StoreIOSURL:     "https://apps.apple.com/app/id1",
		ForbiddenClaims: []string{"free forever"},
	}
	ok := func(req model.DraftRequest) model.DraftRequest {
		if req.Kind == "" {
			req.Kind = "post"
		}
		return req
	}

	errs, warns := ValidateDraft(proj, ok(model.DraftRequest{
		Platform: "bluesky", Text: "Kiran 1.2 is out https://apps.apple.com/app/id1", MediaPath: "kiran/icon.png",
	}))
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("clean draft: errs=%v warns=%v", errs, warns)
	}

	errs, _ = ValidateDraft(proj, ok(model.DraftRequest{Platform: "x", Text: strings.Repeat("a", 281)}))
	if len(errs) == 0 {
		t.Error("281 chars on X should fail")
	}

	errs, _ = ValidateDraft(proj, ok(model.DraftRequest{Platform: "bluesky", Text: "Kiran is Free Forever!"}))
	if len(errs) == 0 || !strings.Contains(errs[0], "forbidden claim") {
		t.Errorf("forbidden claim not caught: %v", errs)
	}

	errs, _ = ValidateDraft(proj, ok(model.DraftRequest{Platform: "youtube", Kind: "short", Text: "desc"}))
	if len(errs) != 2 {
		t.Errorf("youtube without title and media: want 2 errors, got %v", errs)
	}

	errs, _ = ValidateDraft(proj, ok(model.DraftRequest{Platform: "reddit", Text: "hi"}))
	if len(errs) != 2 {
		t.Errorf("reddit post without reply kind and url: want 2 errors, got %v", errs)
	}

	_, warns = ValidateDraft(proj, ok(model.DraftRequest{Platform: "x", Text: "no link, no picture"}))
	if len(warns) != 2 {
		t.Errorf("want link and visual warnings, got %v", warns)
	}
}

func TestFindDuplicates(t *testing.T) {
	recent := []model.Post{
		{ID: 1, Platform: "x", Text: "Kiran 1.2 adds offline mode and a new dark theme. Try it: https://a.b/c"},
		{ID: 2, Platform: "bluesky", Text: "Kiran 1.2 adds offline mode and a new dark theme! Try it https://x.y"},
		{ID: 3, Platform: "x", Text: "Behind the scenes: how we render tiles in Flutter"},
	}
	same, other := FindDuplicates("kiran 1.2 adds offline mode and a new dark theme — try it", "x", recent)
	if len(same) != 1 || same[0].PostID != 1 {
		t.Errorf("same platform duplicates = %+v", same)
	}
	if len(other) != 1 || other[0].PostID != 2 {
		t.Errorf("cross-platform duplicates = %+v", other)
	}
}

func prague(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNextSlot(t *testing.T) {
	loc := prague(t)
	s := Schedule{Loc: loc, WindowStart: 9 * time.Hour, WindowEnd: 20 * time.Hour, Lead: 10 * time.Minute}
	at := func(v string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", v, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}

	// Inside the window and nothing committed: now + lead, rounded to 5 minutes.
	got, _ := s.NextSlot(at("2026-09-14 10:02"), 1, nil)
	if want := at("2026-09-14 10:15"); !got.Equal(want) {
		t.Errorf("free day: got %s want %s", got.In(loc), want)
	}

	// Before the window: window start the same day.
	got, _ = s.NextSlot(at("2026-09-14 06:00"), 1, nil)
	if want := at("2026-09-14 09:00"); !got.Equal(want) {
		t.Errorf("early: got %s want %s", got.In(loc), want)
	}

	// After the window: next morning.
	got, _ = s.NextSlot(at("2026-09-14 21:00"), 1, nil)
	if want := at("2026-09-15 09:00"); !got.Equal(want) {
		t.Errorf("late: got %s want %s", got.In(loc), want)
	}

	// Another project already posted today on this platform: tomorrow.
	committed := []Slot{{PostID: 7, ProjectID: 2, At: at("2026-09-14 09:30")}}
	got, _ = s.NextSlot(at("2026-09-14 10:00"), 1, committed)
	if want := at("2026-09-15 09:00"); !got.Equal(want) {
		t.Errorf("platform busy: got %s want %s", got.In(loc), want)
	}

	// Same project posted two days ago: wait until seven days have passed.
	committed = []Slot{{PostID: 8, ProjectID: 1, At: at("2026-09-12 12:00")}}
	got, _ = s.NextSlot(at("2026-09-14 10:00"), 1, committed)
	if want := at("2026-09-19 12:00"); !got.Equal(want) {
		t.Errorf("project gap: got %s want %s", got.In(loc), want)
	}

	if v := s.Violations(at("2026-09-14 18:00"), 1, committed); len(v) != 1 {
		t.Errorf("violations = %v", v)
	}
}

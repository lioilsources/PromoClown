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
		Platform: "bluesky", Text: "Kiran 1.2 is out https://apps.apple.com/app/id1", MediaPaths: []string{"kiran/icon.png"},
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
	got, _ := s.NextSlot(at("2026-09-14 10:02"), 1, DefaultLimits, nil)
	if want := at("2026-09-14 10:15"); !got.Equal(want) {
		t.Errorf("free day: got %s want %s", got.In(loc), want)
	}

	// Before the window: window start the same day.
	got, _ = s.NextSlot(at("2026-09-14 06:00"), 1, DefaultLimits, nil)
	if want := at("2026-09-14 09:00"); !got.Equal(want) {
		t.Errorf("early: got %s want %s", got.In(loc), want)
	}

	// After the window: next morning.
	got, _ = s.NextSlot(at("2026-09-14 21:00"), 1, DefaultLimits, nil)
	if want := at("2026-09-15 09:00"); !got.Equal(want) {
		t.Errorf("late: got %s want %s", got.In(loc), want)
	}

	// Another project already posted today on this platform: tomorrow.
	committed := []Slot{{PostID: 7, ProjectID: 2, At: at("2026-09-14 09:30")}}
	got, _ = s.NextSlot(at("2026-09-14 10:00"), 1, DefaultLimits, committed)
	if want := at("2026-09-15 09:00"); !got.Equal(want) {
		t.Errorf("platform busy: got %s want %s", got.In(loc), want)
	}

	// Same project posted two days ago: wait until seven days have passed.
	committed = []Slot{{PostID: 8, ProjectID: 1, At: at("2026-09-12 12:00")}}
	got, _ = s.NextSlot(at("2026-09-14 10:00"), 1, DefaultLimits, committed)
	if want := at("2026-09-19 12:00"); !got.Equal(want) {
		t.Errorf("project gap: got %s want %s", got.In(loc), want)
	}

	if v := s.Violations(at("2026-09-14 18:00"), 1, DefaultLimits, committed); len(v) != 1 {
		t.Errorf("violations = %v", v)
	}
}

func TestMediaLimits(t *testing.T) {
	four := []string{"a.png", "b.png", "c.png", "d.png"}
	cases := []struct {
		name     string
		platform string
		paths    []string
		want     string // substring of the expected problem, "" for none
	}{
		{"four images on X", "x", four, ""},
		{"five images on X", "x", append(append([]string{}, four...), "e.png"), "at most 4 images"},
		{"four on bluesky", "bluesky", four, ""},
		{"two videos", "x", []string{"a.mp4", "b.mp4"}, "only attachment"},
		{"video with images", "x", []string{"a.mp4", "b.png"}, "only attachment"},
		{"one video on youtube", "youtube", []string{"a.mp4"}, ""},
		{"two on youtube", "youtube", []string{"a.mp4", "b.mp4"}, "one attachment"},
		{"media on reddit", "reddit", []string{"a.png"}, "no media"},
		{"same file twice", "x", []string{"a.png", "a.png"}, "attached twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(mediaProblems(tc.platform, tc.paths), "; ")
			if tc.want == "" && got != "" {
				t.Fatalf("unexpected problem: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("problems = %q, want something about %q", got, tc.want)
			}
		})
	}
}

// A project allowed several posts a day fills the day, and a project on
// another account is not blocked by it.
func TestPerAccountLimits(t *testing.T) {
	loc := prague(t)
	s := Schedule{Loc: loc, WindowStart: 9 * time.Hour, WindowEnd: 20 * time.Hour, Lead: 10 * time.Minute}
	at := func(v string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", v, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	lim := Limits{PerDay: 3, Gap: 0}

	day := []Slot{
		{PostID: 1, ProjectID: 7, At: at("2026-09-14 10:00")},
		{PostID: 2, ProjectID: 7, At: at("2026-09-14 14:00")},
	}
	got, err := s.NextSlot(at("2026-09-14 09:00"), 7, lim, day)
	if err != nil {
		t.Fatal(err)
	}
	if want := at("2026-09-14 09:10"); !got.Equal(want) {
		t.Errorf("third post of the day = %s, want %s", got, want)
	}

	day = append(day, Slot{PostID: 3, ProjectID: 7, At: at("2026-09-14 17:00")})
	got, err = s.NextSlot(at("2026-09-14 09:00"), 7, lim, day)
	if err != nil {
		t.Fatal(err)
	}
	if got.In(loc).Day() != 15 {
		t.Errorf("fourth post = %s, want the next day", got.In(loc))
	}

	// The default rules still hold one post a day, a week apart.
	if _, err := s.NextSlot(at("2026-09-14 09:00"), 7, DefaultLimits, day[:1]); err != nil {
		t.Fatal(err)
	}
	if v := s.Violations(at("2026-09-14 18:00"), 7, lim, day); len(v) != 1 {
		t.Errorf("violations = %v, want the day to be full", v)
	}
}

// A project whose call to action lives in the account profile must not be
// nagged about every post that carries no link.
func TestLinkInProfileSilencesTheLinkWarning(t *testing.T) {
	p := model.Project{Slug: "tsumiki", Name: "Tsumiki", WebsiteURL: "https://t.me/bot"}
	req := model.DraftRequest{Platform: "x", Text: "Tribute to @artist", MediaPaths: []string{"a.png"}}

	_, warns := ValidateDraft(p, req)
	if !hasWarning(warns, "no store or website link") {
		t.Fatalf("warnings = %v, want the link warning by default", warns)
	}

	p.LinkInProfile = true
	if _, warns := ValidateDraft(p, req); hasWarning(warns, "no store or website link") {
		t.Fatalf("warnings = %v, want no link warning", warns)
	}
}

func hasWarning(warns []string, needle string) bool {
	for _, w := range warns {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

package core

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/lioilsources/promoclown/internal/model"
)

// The deterministic half of PROMO_RULES.md. The agent is told the rules; this
// file makes sure a draft that breaks the measurable ones never reaches
// Telegram, whatever the model thought it was doing.

var (
	urlRe     = regexp.MustCompile(`https?://[^\s<>"']+`)
	hashtagRe = regexp.MustCompile(`(^|\s)#[\p{L}\p{N}_]+`)
	wordRe    = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

const (
	xMaxWeighted        = 280
	xURLWeight          = 23
	blueskyMaxGraphemes = 300
	youtubeMaxTitle     = 100
	youtubeMaxDescBytes = 5000
	redditMaxReply      = 10000

	// DuplicateThreshold is the word-shingle Jaccard similarity above which two
	// texts count as the same post.
	DuplicateThreshold = 0.75
)

// XLength is the weighted length X counts against its 280 limit (twitter-text
// v3 config): every URL weighs 23, code points outside the Latin and general
// punctuation ranges weigh 2. Emoji sequences are over-counted slightly, which
// only ever errs on the safe side.
func XLength(s string) int {
	n := 0
	rest := urlRe.ReplaceAllStringFunc(s, func(string) string {
		n += xURLWeight
		return ""
	})
	for _, r := range rest {
		if r <= 4351 || (r >= 8192 && r <= 8205) || (r >= 8208 && r <= 8223) || (r >= 8242 && r <= 8247) {
			n++
		} else {
			n += 2
		}
	}
	return n
}

// GraphemeLength approximates user-perceived characters, which is what the
// Bluesky 300 limit counts: combining marks, variation selectors, skin tone
// modifiers and ZWJ-joined emoji do not add to the count, flag pairs count once.
func GraphemeLength(s string) int {
	n := 0
	joined := false
	regional := 0
	for _, r := range s {
		switch {
		case r == 0x200D:
			joined = true
			continue
		case unicode.In(r, unicode.Mn, unicode.Me), r == 0xFE0E, r == 0xFE0F, r >= 0x1F3FB && r <= 0x1F3FF:
			continue
		case r >= 0x1F1E6 && r <= 0x1F1FF:
			regional++
			if regional%2 == 0 {
				continue
			}
		}
		if joined {
			joined = false
			continue
		}
		n++
	}
	return n
}

func countEmoji(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.So, r) && r >= 0x2600 {
			n++
		}
	}
	return n
}

// ValidateDraft checks a draft against the platform limits and the project's
// rules. Errors block the draft; warnings travel with it to the approver.
func ValidateDraft(p model.Project, req model.DraftRequest) (errs, warns []string) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		errs = append(errs, "text is empty")
	}
	if !contains(model.Platforms, req.Platform) {
		errs = append(errs, fmt.Sprintf("unknown platform %q (use %s)", req.Platform, strings.Join(model.Platforms, ", ")))
		return errs, warns
	}
	if !contains(model.Kinds, req.Kind) {
		errs = append(errs, fmt.Sprintf("unknown kind %q (use %s)", req.Kind, strings.Join(model.Kinds, ", ")))
	}

	switch req.Platform {
	case model.PlatformX:
		if n := XLength(text); n > xMaxWeighted {
			errs = append(errs, fmt.Sprintf("text is %d/%d weighted characters on X (URLs count as 23)", n, xMaxWeighted))
		}
	case model.PlatformBluesky:
		if n := GraphemeLength(text); n > blueskyMaxGraphemes {
			errs = append(errs, fmt.Sprintf("text is %d/%d characters on Bluesky", n, blueskyMaxGraphemes))
		}
	case model.PlatformYouTube:
		title := strings.TrimSpace(req.Title)
		switch {
		case title == "":
			errs = append(errs, "youtube needs --title")
		case GraphemeLength(title) > youtubeMaxTitle:
			errs = append(errs, fmt.Sprintf("youtube title is %d/%d characters", GraphemeLength(title), youtubeMaxTitle))
		}
		if len(text) > youtubeMaxDescBytes {
			errs = append(errs, fmt.Sprintf("youtube description is %d/%d bytes", len(text), youtubeMaxDescBytes))
		}
		if strings.ContainsAny(title+text, "<>") {
			errs = append(errs, "youtube rejects < and > in title and description")
		}
		if req.MediaPath == "" {
			errs = append(errs, "youtube needs a video (--media)")
		}
	case model.PlatformReddit:
		if req.Kind != "reply" {
			errs = append(errs, "reddit is replies only in v1 (--kind reply --reply-to <url>)")
		}
		if !strings.Contains(req.ReplyToURL, "reddit.com/") {
			errs = append(errs, "reddit reply needs --reply-to with the reddit.com thread or comment URL")
		}
		if len(text) > redditMaxReply {
			errs = append(errs, fmt.Sprintf("reddit reply is %d/%d characters", len(text), redditMaxReply))
		}
	}
	if req.Kind == "reply" && req.Platform != model.PlatformReddit {
		errs = append(errs, "replies are only supported for reddit")
	}

	lower := strings.ToLower(text + "\n" + req.Title)
	for _, claim := range p.ForbiddenClaims {
		if c := strings.ToLower(strings.TrimSpace(claim)); c != "" && strings.Contains(lower, c) {
			errs = append(errs, fmt.Sprintf("text makes a forbidden claim for %s: %q", p.Slug, claim))
		}
	}

	if req.Platform != model.PlatformReddit {
		if links := p.Links(); len(links) > 0 && !containsAny(text, links) {
			warns = append(warns, "no store or website link of the project in the text")
		}
		if req.MediaPath == "" {
			warns = append(warns, "no visual attached")
		}
	}
	if n := len(hashtagRe.FindAllString(text, -1)); n > 3 {
		warns = append(warns, fmt.Sprintf("%d hashtags", n))
	}
	if n := countEmoji(text); n > 3 {
		warns = append(warns, fmt.Sprintf("%d emoji", n))
	}
	return errs, warns
}

// Similarity is the Jaccard index of the two texts' word bigrams (unigrams for
// very short texts), ignoring case, URLs and punctuation.
func Similarity(a, b string) float64 {
	sa, sb := shingles(a), shingles(b)
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for k := range sa {
		if sb[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

func shingles(s string) map[string]bool {
	words := wordRe.FindAllString(strings.ToLower(urlRe.ReplaceAllString(s, " ")), -1)
	set := make(map[string]bool)
	if len(words) < 4 {
		for _, w := range words {
			set[w] = true
		}
		return set
	}
	for i := 0; i+1 < len(words); i++ {
		set[words[i]+" "+words[i+1]] = true
	}
	return set
}

// Duplicate is an earlier post whose text is too close to a new one.
type Duplicate struct {
	PostID   int64
	Platform string
	Score    float64
}

// FindDuplicates splits near-duplicates of text into those on the same
// platform (a repeat to the same audience) and those elsewhere (cross-posting).
func FindDuplicates(text, platform string, recent []model.Post) (same, other []Duplicate) {
	for _, p := range recent {
		score := Similarity(text, p.Text)
		if score < DuplicateThreshold {
			continue
		}
		d := Duplicate{PostID: p.ID, Platform: p.Platform, Score: score}
		if p.Platform == platform {
			same = append(same, d)
		} else {
			other = append(other, d)
		}
	}
	return same, other
}

// Slot is a post already committed to a time on one platform.
type Slot struct {
	PostID    int64
	ProjectID int64
	At        time.Time
}

// Schedule turns the frequency rules into concrete publication times:
// at most one post per platform per local calendar day, and at most one post
// per project per platform in any seven days.
type Schedule struct {
	Loc         *time.Location
	WindowStart time.Duration // offset from local midnight, e.g. 9h
	WindowEnd   time.Duration // e.g. 20h
	Lead        time.Duration // minimum gap between approval and publication
}

const projectGap = 7 * 24 * time.Hour

type conflict struct {
	slot       Slot
	sameDay    bool
	projectGap bool
}

func (s Schedule) conflicts(at time.Time, projectID int64, committed []Slot) []conflict {
	var out []conflict
	local := at.In(s.Loc)
	for _, c := range committed {
		cl := c.At.In(s.Loc)
		k := conflict{slot: c}
		k.sameDay = cl.Year() == local.Year() && cl.YearDay() == local.YearDay()
		if c.ProjectID == projectID {
			d := at.Sub(c.At)
			k.projectGap = d < projectGap && d > -projectGap
		}
		if k.sameDay || k.projectGap {
			out = append(out, k)
		}
	}
	return out
}

// Violations lists the rules a post of projectID at time at would break.
func (s Schedule) Violations(at time.Time, projectID int64, committed []Slot) []string {
	var out []string
	for _, c := range s.conflicts(at, projectID, committed) {
		when := c.slot.At.In(s.Loc).Format("2006-01-02 15:04")
		if c.sameDay {
			out = append(out, fmt.Sprintf("platform already has #%d that day (%s)", c.slot.PostID, when))
		}
		if c.projectGap {
			out = append(out, fmt.Sprintf("project already has #%d on this platform within 7 days (%s)", c.slot.PostID, when))
		}
	}
	return out
}

// OutsideWindow reports whether at falls outside the daily posting window.
func (s Schedule) OutsideWindow(at time.Time) bool {
	local := at.In(s.Loc)
	return local.Before(s.at(local, s.WindowStart)) || !local.Before(s.at(local, s.WindowEnd))
}

// NextSlot returns the earliest time inside the posting window, at least Lead
// after now, that breaks no frequency rule.
func (s Schedule) NextSlot(now time.Time, projectID int64, committed []Slot) (time.Time, error) {
	earliest := roundUp(now.Add(s.Lead).In(s.Loc))
	for i := 0; i < 120; i++ {
		day := earliest.AddDate(0, 0, i)
		start, end := s.at(day, s.WindowStart), s.at(day, s.WindowEnd)
		cand := start
		if cand.Before(earliest) {
			cand = earliest
		}
		for cand.Before(end) {
			cs := s.conflicts(cand, projectID, committed)
			if len(cs) == 0 {
				return cand.UTC(), nil
			}
			next, busy := cand, false
			for _, c := range cs {
				if c.sameDay {
					busy = true
					break
				}
				// Too close to the project's other post: earliest escape is
				// seven days after it, possibly later the same day.
				if t := c.slot.At.Add(projectGap); t.After(next) {
					next = t
				}
			}
			if busy || !next.After(cand) {
				break
			}
			cand = roundUp(next.In(s.Loc))
		}
	}
	return time.Time{}, fmt.Errorf("no free slot in the next 120 days")
}

// at is the wall-clock offset on t's local day, correct across DST changes.
func (s Schedule) at(t time.Time, offset time.Duration) time.Time {
	t = t.In(s.Loc)
	return time.Date(t.Year(), t.Month(), t.Day(), int(offset.Hours()), int(offset.Minutes())%60, 0, 0, s.Loc)
}

// roundUp moves t to the next whole five minutes so slots look chosen.
func roundUp(t time.Time) time.Time {
	if r := t.Truncate(5 * time.Minute); r.Before(t) {
		return r.Add(5 * time.Minute)
	}
	return t
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

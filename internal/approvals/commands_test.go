package approvals

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Command
	}{
		{"ok 12", Command{Verb: "ok", ID: 12}},
		{"OK #12", Command{Verb: "ok", ID: 12}},
		{"/ok@ol1n_promo_approve_bot 12", Command{Verb: "ok", ID: 12}},
		{"ok 12 at 2026-09-14T10:00", Command{Verb: "ok", ID: 12, At: "2026-09-14T10:00"}},
		{"ok 12 2026-09-14 10:00", Command{Verb: "ok", ID: 12, At: "2026-09-14 10:00"}},
		{"no 12 too salesy", Command{Verb: "no", ID: 12, Text: "too salesy"}},
		{"ne 3", Command{Verb: "no", ID: 3}},
		{"edit 12 First line\nSecond line", Command{Verb: "edit", ID: 12, Text: "First line\nSecond line"}},
		{"done 7 https://www.reddit.com/r/x/comments/1/_/abc", Command{Verb: "done", ID: 7, Text: "https://www.reddit.com/r/x/comments/1/_/abc"}},
		{"list", Command{Verb: "list"}},
		{"/start", Command{Verb: "help"}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"ok", "edit 12", "publish 12 now", "12", ""} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestCallback(t *testing.T) {
	data := CallbackData("ok", 123456, 42)
	if len(data) > 64 {
		t.Fatalf("callback data %q exceeds 64 bytes", data)
	}
	got, err := ParseCallback(data)
	if err != nil || got != (Command{Verb: "ok", ID: 123456, Revision: 42}) {
		t.Fatalf("ParseCallback(%q) = %+v, %v", data, got, err)
	}
	for _, bad := range []string{"edit:1:1", "ok:1", "ok:x:1", "ok:1:0"} {
		if _, err := ParseCallback(bad); err == nil {
			t.Errorf("ParseCallback(%q) should fail", bad)
		}
	}
}

func TestVisibleLen(t *testing.T) {
	if n := visibleLen("<b>#1</b> a &amp; b"); n != len("#1 a & b") {
		t.Errorf("visibleLen = %d", n)
	}
	if n := visibleLen("🚀"); n != 2 {
		t.Errorf("emoji should count as 2 UTF-16 units, got %d", n)
	}
	if !strings.Contains(fitMessage(strings.Repeat("x", 5000)), "…") {
		t.Error("fitMessage did not truncate")
	}
}

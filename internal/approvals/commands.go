package approvals

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Command is one instruction from the approver, typed or from a button.
type Command struct {
	Verb     string // ok | no | edit | title | retry | done | show | list | help
	ID       int64
	Revision int64  // set by buttons; 0 means "the last preview sent"
	At       string // requested publication time
	Text     string // new text, title, rejection reason or published URL
}

var aliases = map[string]string{
	"ok": "ok", "yes": "ok", "ano": "ok", "approve": "ok",
	"no": "no", "ne": "no", "reject": "no",
	"edit": "edit", "uprav": "edit",
	"title": "title", "titulek": "title",
	"retry": "retry", "znovu": "retry",
	"done": "done", "hotovo": "done",
	"show": "show", "ukaz": "show",
	"list": "list", "drafts": "list",
	"help": "help", "start": "help", "pomoc": "help",
}

// (?s) lets edit text span lines; @bot covers /ok@ol1n_promo_approve_bot.
var commandRe = regexp.MustCompile(`(?s)^\s*/?([A-Za-z]+)(?:@\w+)?(?:\s+#?(\d+))?(?:\s+(.*?))?\s*$`)

// Parse reads a typed command such as "ok 12 at 2026-09-14T10:00".
func Parse(input string) (Command, error) {
	m := commandRe.FindStringSubmatch(input)
	if m == nil {
		return Command{}, fmt.Errorf("tomu nerozumím")
	}
	verb, ok := aliases[strings.ToLower(m[1])]
	if !ok {
		return Command{}, fmt.Errorf("neznámý příkaz %q", m[1])
	}
	cmd := Command{Verb: verb, Text: m[3]}
	if m[2] != "" {
		cmd.ID, _ = strconv.ParseInt(m[2], 10, 64)
	}

	switch verb {
	case "list", "help":
		return Command{Verb: verb}, nil
	}
	if cmd.ID == 0 {
		return Command{}, fmt.Errorf("%s potřebuje číslo postu, např. %s 12", verb, verb)
	}
	switch verb {
	case "ok", "retry":
		at := strings.TrimSpace(cmd.Text)
		if len(at) > 3 && strings.EqualFold(at[:3], "at ") {
			at = strings.TrimSpace(at[3:])
		}
		cmd.At, cmd.Text = at, ""
	case "edit", "title":
		if strings.TrimSpace(cmd.Text) == "" {
			return Command{}, fmt.Errorf("%s %d potřebuje nový text", verb, cmd.ID)
		}
	case "show":
		cmd.Text = ""
	}
	return cmd, nil
}

// CallbackData encodes a button press; it must stay under Telegram's 64 bytes.
func CallbackData(verb string, id, revision int64) string {
	return fmt.Sprintf("%s:%d:%d", verb, id, revision)
}

// ParseCallback reads what CallbackData wrote.
func ParseCallback(data string) (Command, error) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || (parts[0] != "ok" && parts[0] != "no") {
		return Command{}, fmt.Errorf("unknown callback %q", data)
	}
	id, err1 := strconv.ParseInt(parts[1], 10, 64)
	rev, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || id <= 0 || rev <= 0 {
		return Command{}, fmt.Errorf("bad callback %q", data)
	}
	return Command{Verb: parts[0], ID: id, Revision: rev}, nil
}

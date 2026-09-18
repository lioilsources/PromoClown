package approvals

import "testing"

func TestParseCaption(t *testing.T) {
	cases := []struct {
		caption               string
		project, credit, note string
	}{
		{"@artist", "tsumiki", "@artist", ""},
		{"@artist krásná kompozice", "tsumiki", "@artist", "krásná kompozice"},
		{"#kirian @artist", "kirian", "@artist", ""},
		{"@artist #kirian z Redditu", "kirian", "@artist", "z Redditu"},
		{"https://x.com/artist", "tsumiki", "@artist", ""},
		{"jen poznámka", "tsumiki", "", "jen poznámka"},
		{"", "tsumiki", "", ""},
		// Only the first handle is the author; a second one stays in the note.
		{"@artist a @kamarad", "tsumiki", "@artist", "a @kamarad"},
	}
	for _, tc := range cases {
		project, credit, note := parseCaption(tc.caption, "tsumiki")
		if project != tc.project || credit != tc.credit || note != tc.note {
			t.Errorf("parseCaption(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.caption, project, credit, note, tc.project, tc.credit, tc.note)
		}
	}
}

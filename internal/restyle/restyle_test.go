package restyle

import (
	"encoding/json"
	"strings"
	"testing"
)

// The 42 artist styles of the third wave ("Kandidátský běh") in Ol1nLLM
// docs/style-matrix.md — the ones a model demonstrably took over.
var tier1IDs = []string{
	"davinci", "davinci-chalk", "picasso-blue", "picasso-rose", "picasso-cubist",
	"basquiat", "hockney-pool", "monet", "kahlo", "goya-black", "goya-caprichos",
	"kandinsky-early", "vangogh-arles", "vangogh-saintremy", "lautrec-poster",
	"lautrec-cabaret", "mucha-slav-epic", "kubista", "schiele", "klimt-golden",
	"vermeer", "botticelli", "elgreco", "munch", "matisse-fauve", "matisse-cutout",
	"gauguin", "cezanne", "seurat", "hopper", "warhol", "lichtenstein", "haring",
	"bacon", "rivera", "chagall", "dali", "magritte", "lempicka", "beardsley",
	"lada", "josef-capek",
}

func TestCatalog(t *testing.T) {
	styles := Styles()
	if len(styles) == 0 {
		t.Fatal("styles.json is empty")
	}
	seen := map[string]bool{}
	tier1 := 0
	for _, s := range styles {
		switch {
		case s.ID == "":
			t.Errorf("style with empty id: %+v", s)
		case s.Label == "":
			t.Errorf("%s: empty label", s.ID)
		case s.Group == "":
			t.Errorf("%s: empty group", s.ID)
		case strings.TrimSpace(s.Block) == "":
			t.Errorf("%s: empty block", s.ID)
		}
		if s.Tier != 1 && s.Tier != 2 {
			t.Errorf("%s: tier %d, want 1 or 2", s.ID, s.Tier)
		}
		if s.Tier == 1 {
			tier1++
		}
		if seen[s.ID] {
			t.Errorf("%s: duplicate id", s.ID)
		}
		seen[s.ID] = true
	}
	if tier1 != len(tier1IDs) {
		t.Errorf("tier 1 count = %d, want %d", tier1, len(tier1IDs))
	}
	for _, id := range tier1IDs {
		s, ok := StyleByID(id)
		if !ok {
			t.Errorf("%s: missing from the catalog", id)
			continue
		}
		if s.Tier != 1 {
			t.Errorf("%s: tier %d, want 1", id, s.Tier)
		}
	}
}

func TestStylePrompt(t *testing.T) {
	s, ok := StyleByID("vangogh-arles")
	if !ok {
		t.Fatal("vangogh-arles missing")
	}
	got := s.Prompt()
	if !strings.HasPrefix(got, mediumHead+", ") {
		t.Errorf("prompt does not start with the medium: %q", got)
	}
	if !strings.HasSuffix(got, s.Block) {
		t.Errorf("prompt does not end with the style block: %q", got)
	}
}

func TestPickDistinct(t *testing.T) {
	for _, n := range []int{1, 4, 12} {
		got := Pick(n, NewRand(7))
		if len(got) != n {
			t.Fatalf("Pick(%d) returned %d styles", n, len(got))
		}
		seen := map[string]bool{}
		for _, s := range got {
			if seen[s.ID] {
				t.Errorf("Pick(%d) repeated %s", n, s.ID)
			}
			seen[s.ID] = true
		}
	}
	if got := Pick(0, NewRand(1)); got != nil {
		t.Errorf("Pick(0) = %v, want nil", got)
	}
	// One style per painter caps a draw below the catalog size: asking for
	// everything returns one style per artist, not every variant of each.
	artists := map[string]bool{}
	for _, s := range catalog {
		artists[artistOf(s.ID)] = true
	}
	if got := Pick(len(catalog)+5, NewRand(1)); len(got) != len(artists) {
		t.Errorf("Pick(more than the catalog) = %d styles, want one per artist (%d)", len(got), len(artists))
	}
}

func TestPickIsReproducible(t *testing.T) {
	a, b := Pick(4, NewRand(42)), Pick(4, NewRand(42))
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("draw %d: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
	if Pick(4, nil) == nil {
		t.Error("Pick with a nil source returned nothing")
	}
}

// Over many single draws a tier-1 style must come up far more often than a
// tier-2 one: weight 4 against 1, against a catalog that is 42 to 48.
func TestPickFavoursTier1(t *testing.T) {
	const draws = 20000
	rnd := NewRand(3)
	tier1 := 0
	for range draws {
		if Pick(1, rnd)[0].Tier == 1 {
			tier1++
		}
	}
	var w1, w2 int
	for _, s := range catalog {
		if s.Tier == 1 {
			w1 += tierWeight(1)
		} else {
			w2 += tierWeight(2)
		}
	}
	want := float64(w1) / float64(w1+w2)
	got := float64(tier1) / draws
	if got < want-0.02 || got > want+0.02 {
		t.Errorf("tier-1 share = %.3f, want about %.3f", got, want)
	}
}

func TestSnapToBucket(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		want bucket
	}{
		{"square", 2000, 2000, bucket{1024, 1024}},
		{"phone portrait 3:4", 3024, 4032, bucket{896, 1152}},
		{"phone landscape 4:3", 4032, 3024, bucket{1152, 896}},
		{"portrait 2:3", 2000, 3000, bucket{832, 1216}},
		{"landscape 3:2", 3000, 2000, bucket{1216, 832}},
		{"tall 9:16", 1080, 1920, bucket{768, 1344}},
		{"wide 16:9", 1920, 1080, bucket{1344, 768}},
		{"degenerate", 0, 100, fallbackBucket},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snapToBucket(tt.w, tt.h); got != tt.want {
				t.Errorf("snapToBucket(%d, %d) = %v, want %v", tt.w, tt.h, got, tt.want)
			}
		})
	}
}

func TestLatentForHeaders(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0, 0, 0x07, 0x80, 0, 0, 0x04, 0x38) // 1920x1080
	if got := latentFor(png); got != (bucket{1344, 768}) {
		t.Errorf("PNG 1920x1080 -> %v", got)
	}
	// SOI, a COM segment to skip, then SOF0 with 1080x1920.
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xFE, 0x00, 0x04, 'h', 'i', 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x07, 0x80, 0x04, 0x38}
	if got := latentFor(jpeg); got != (bucket{768, 1344}) {
		t.Errorf("JPEG 1080x1920 -> %v", got)
	}
	if got := latentFor([]byte("not an image")); got != fallbackBucket {
		t.Errorf("unreadable header -> %v, want the fallback", got)
	}
}

func TestPatchWorkflow(t *testing.T) {
	style, ok := StyleByID("munch")
	if !ok {
		t.Fatal("munch missing")
	}
	wf, err := patchWorkflow("photo_42.png", style.Prompt(), bucket{896, 1152}, 123456)
	if err != nil {
		t.Fatalf("patchWorkflow: %v", err)
	}

	tests := []struct {
		node, input, want string
	}{
		{"1", "ckpt_name", `"` + Checkpoint + `"`},
		{"2", "image", `"photo_42.png"`},
		{"7", "text", string(rawJSON(style.Prompt()))},
		{"8", "text", string(rawJSON(negativePrompt))},
		{"3", "width", "896"},
		{"3", "height", "1152"},
		{"14", "width", "896"},
		{"14", "height", "1152"},
		{"14", "batch_size", "1"},
		{"12", "ip_weight", "0.6"},
		{"13", "strength", "0.75"},
		{"15", "seed", "123456"},
		{"18", "seed", "123456"},
	}
	for _, tt := range tests {
		t.Run(tt.node+"."+tt.input, func(t *testing.T) {
			n, ok := wf[tt.node]
			if !ok {
				t.Fatalf("node %s missing from the workflow", tt.node)
			}
			got, ok := n.Inputs[tt.input]
			if !ok {
				t.Fatalf("node %s (%s) has no input %q", tt.node, n.ClassType, tt.input)
			}
			if string(got) != tt.want {
				t.Errorf("node %s %s = %s, want %s", tt.node, tt.input, got, tt.want)
			}
		})
	}

	// Nothing may keep a placeholder, and the graph must still be valid JSON.
	buf, err := json.Marshal(wf)
	if err != nil {
		t.Fatalf("marshal patched workflow: %v", err)
	}
	for _, ph := range []string{phPrompt, phNegative, phImage, phCheckpoint} {
		if strings.Contains(string(buf), ph) {
			t.Errorf("%s survived the patch", ph)
		}
	}
	if !strings.Contains(string(buf), `"class_type":"KSampler"`) {
		t.Error("the patched workflow lost its class_type fields")
	}
}

func TestPatchWorkflowSeedsDiffer(t *testing.T) {
	a, err := patchWorkflow("x.png", "p", fallbackBucket, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := patchWorkflow("x.png", "p", fallbackBucket, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(a["15"].Inputs["seed"]) == string(b["15"].Inputs["seed"]) {
		t.Error("two seeds produced the same KSampler seed")
	}
}

func TestExecutionError(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`["execution_start", {}]`),
		json.RawMessage(`["execution_error", {"exception_message": "InsightFace: No face detected.\nTraceback..."}]`),
	}
	if got := executionError(msgs); got != "ComfyUI: InsightFace: No face detected." {
		t.Errorf("executionError = %q", got)
	}
	if got := executionError(nil); got == "" {
		t.Error("executionError(nil) returned an empty reason")
	}
}

// Four images by "four painters" must not spend two slots on one painter's two
// periods — picasso-blue next to picasso-rose is the case that prompted this.
func TestPickOneStylePerArtist(t *testing.T) {
	for seed := uint64(1); seed <= 500; seed++ {
		got := Pick(4, NewRand(seed))
		if len(got) != 4 {
			t.Fatalf("seed %d: %d styles", seed, len(got))
		}
		seen := map[string]string{}
		for _, s := range got {
			a := artistOf(s.ID)
			if prev, dup := seen[a]; dup {
				t.Fatalf("seed %d: %s and %s are both %s", seed, prev, s.ID, a)
			}
			seen[a] = s.ID
		}
	}
}

func TestArtistOf(t *testing.T) {
	for id, want := range map[string]string{
		"picasso-blue":    "picasso",
		"picasso-rose":    "picasso",
		"vangogh-arles":   "vangogh",
		"chineseink":      "chineseink",
		"josef-capek":     "josef",
		"mucha-slav-epic": "mucha",
	} {
		if got := artistOf(id); got != want {
			t.Errorf("artistOf(%q) = %q, want %q", id, got, want)
		}
	}
}

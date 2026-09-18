// Package restyle turns one photograph into several restyled variants on a
// ComfyUI server: the pose comes from a depth ControlNet over the source, the
// face from InstantID, and a painter/era prompt block does the rest.
//
// The workflow graph and the style blocks are the ones the Tsumiki app
// measured (MangaPrompts); the settings that measurement settled — SDXL base
// 1.0 as the checkpoint, InstantID ip_weight 0.6, the "illustration" medium —
// are constants here rather than knobs.
package restyle

import (
	"bytes"
	"context"
	crand "crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// styles.json is generated from MangaPrompts lib/config/restyle_styles.dart
// (the Dart is never read at runtime); the tier comes from Ol1nLLM
// docs/style-matrix.md.
//
//go:embed styles.json
var stylesJSON []byte

// Vendored copy of MangaPrompts assets/comfyui/sdxl_restyle.api.json — SDXL +
// InstantID (identity) + xinsir union depth ControlNet (pose). Re-copy it from
// there rather than editing it in place.
//
//go:embed sdxl_restyle.api.json
var workflowJSON []byte

const (
	// Checkpoint is the one that lets a style through. Juggernaut ignores the
	// style blocks and Animagine destroys the identity; both were measured and
	// rejected (MangaPrompts docs/restyle-rollout-results.md).
	Checkpoint = "sd_xl_base_1.0.safetensors"

	// IPWeight is InstantID's hold on the face. 0.8 was measured worse — the
	// identity then overpowers the style.
	IPWeight = 0.6

	// DepthStrength is the depth ControlNet's hold on the pose.
	DepthStrength = 0.75

	// DefaultBaseURL is ComfyUI on the Spark, reached over the loopback.
	DefaultBaseURL = "http://127.0.0.1:8188"

	// DefaultJobTimeout bounds one variant, queue wait included. A variant
	// renders in 66-120 s on the Spark's single GPU.
	DefaultJobTimeout = 300 * time.Second
)

// The "illustration" medium of restyle_styles.dart. The medium sentence goes
// first because CLIP weights early tokens most, and the style block is
// appended after it. "a fully clothed person" is not decoration: a depth map
// carries a silhouette but not its clothes.
const (
	mediumHead = "a painted illustration of a fully clothed person, artwork"

	negativePrompt = "photograph, photorealistic, photo, 3d render, " +
		"nude, naked, nsfw, bad quality, worst quality, low quality, jpeg artifacts, blurry, " +
		"watermark, deformed, disfigured, bad anatomy, bad hands"
)

// Style is one entry of the restyle catalog.
type Style struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"`
	// Block is the prompt fragment describing the look.
	Block string `json:"block"`
	// Tier 1 is an artist style a model demonstrably took over in the third
	// wave of Ol1nLLM's style matrix; tier 2 is the rest of the catalog.
	Tier int `json:"tier"`
}

// Prompt is the positive prompt for this style: medium first, style block last.
func (s Style) Prompt() string { return mediumHead + ", " + s.Block }

var catalog = loadCatalog()

func loadCatalog() []Style {
	var out []Style
	if err := json.Unmarshal(stylesJSON, &out); err != nil {
		panic("restyle: styles.json: " + err.Error())
	}
	return out
}

// Styles returns the whole catalog in its original order.
func Styles() []Style { return append([]Style(nil), catalog...) }

// StyleByID looks a style up by its catalog id.
func StyleByID(id string) (Style, bool) {
	for _, s := range catalog {
		if s.ID == id {
			return s, true
		}
	}
	return Style{}, false
}

// tierWeight ranks how likely a style is to be drawn: a tier-1 style comes up
// four times as often as any other.
func tierWeight(tier int) int {
	if tier == 1 {
		return 4
	}
	return 1
}

// Pick draws n distinct styles, weighted by tier. A nil rnd draws from a
// crypto-seeded source; pass NewRand to repeat a draw.
func Pick(n int, rnd *rand.Rand) []Style {
	if n <= 0 {
		return nil
	}
	if rnd == nil {
		rnd = randomRand()
	}
	pool := Styles()
	if n > len(pool) {
		n = len(pool)
	}
	out := make([]Style, 0, n)
	for len(out) < n && len(pool) > 0 {
		total := 0
		for _, s := range pool {
			total += tierWeight(s.Tier)
		}
		r := rnd.IntN(total)
		for i, s := range pool {
			if r -= tierWeight(s.Tier); r < 0 {
				out = append(out, s)
				pool = dropArtist(append(pool[:i:i], pool[i+1:]...), artistOf(s.ID))
				break
			}
		}
	}
	return out
}

// artistOf is the painter behind a style id: the catalog spells variants of one
// painter as "picasso-blue", "picasso-rose", "picasso-cubist". Ids without a
// dash are their own group.
func artistOf(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

// dropArtist removes every remaining style by the same painter, so a set of
// four never spends two of its slots on one artist's two periods.
func dropArtist(pool []Style, artist string) []Style {
	out := pool[:0]
	for _, s := range pool {
		if artistOf(s.ID) != artist {
			out = append(out, s)
		}
	}
	return out
}

// NewRand is a deterministic source for Pick and for the per-variant
// generation seeds, so a whole run can be repeated.
func NewRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

func randomRand() *rand.Rand {
	var b [16]byte
	crand.Read(b[:]) // never fails
	return rand.New(rand.NewPCG(binary.LittleEndian.Uint64(b[:8]), binary.LittleEndian.Uint64(b[8:])))
}

// Standard SDXL training buckets (about 1 MP, multiples of 64). Rendering in
// the bucket closest to the source's aspect keeps the depth hint and the face
// keypoints from being centre-cropped.
type bucket struct{ w, h int }

var sdxlBuckets = []bucket{
	{1024, 1024},
	{896, 1152},
	{1152, 896},
	{832, 1216},
	{1216, 832},
	{768, 1344},
	{1344, 768},
}

// fallbackBucket is the portrait default, which is what most photos of a
// person are anyway.
var fallbackBucket = bucket{832, 1216}

// snapToBucket picks the closest bucket by aspect, compared in log space so
// portrait and landscape deviations weigh the same. Ties keep the earlier one.
func snapToBucket(w, h int) bucket {
	if w <= 0 || h <= 0 {
		return fallbackBucket
	}
	target := math.Log(float64(w) / float64(h))
	best, bestDist := sdxlBuckets[0], math.Inf(1)
	for _, b := range sdxlBuckets {
		if d := math.Abs(math.Log(float64(b.w)/float64(b.h)) - target); d < bestDist {
			best, bestDist = b, d
		}
	}
	return best
}

// latentFor is the bucket for a source photo, read from its header: both
// formats a gallery hands over carry their size in the first few hundred
// bytes, so nothing has to be decoded.
func latentFor(data []byte) bucket {
	if w, h, ok := imageSize(data); ok {
		return snapToBucket(w, h)
	}
	return fallbackBucket
}

func imageSize(data []byte) (int, int, bool) {
	if w, h, ok := pngSize(data); ok {
		return w, h, true
	}
	return jpegSize(data)
}

func pngSize(data []byte) (int, int, bool) {
	if len(data) < 24 || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) || string(data[12:16]) != "IHDR" {
		return 0, 0, false
	}
	w := int(binary.BigEndian.Uint32(data[16:20]))
	h := int(binary.BigEndian.Uint32(data[20:24]))
	return w, h, w > 0 && h > 0
}

func jpegSize(data []byte) (int, int, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, false
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 0, 0, false
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF: // fill byte
			i++
			continue
		case marker == 0xD8, marker == 0x01, marker >= 0xD0 && marker <= 0xD7: // standalone
			i += 2
			continue
		case marker == 0xD9: // end of image before any frame header
			return 0, 0, false
		}
		// SOF0..SOF15 minus the DHT/JPG/DAC markers that share the range.
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+9 > len(data) {
				return 0, 0, false
			}
			h := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			w := int(binary.BigEndian.Uint16(data[i+7 : i+9]))
			return w, h, w > 0 && h > 0
		}
		i += 2 + int(binary.BigEndian.Uint16(data[i+2:i+4]))
	}
	return 0, 0, false
}

// Placeholders the template carries; every one of them must be filled.
const (
	phPrompt     = "__PROMPT__"
	phNegative   = "__NEGATIVE__"
	phImage      = "__IMAGE__"
	phCheckpoint = "__CKPT__"
)

type node struct {
	ClassType string                     `json:"class_type"`
	Meta      json.RawMessage            `json:"_meta,omitempty"`
	Inputs    map[string]json.RawMessage `json:"inputs"`
}

// patchWorkflow fills the template in for one variant. The placeholder
// substitution and the latent override are a port of prepare_workflow in
// MangaPrompts tgbot/comfy.py: the reference photo is letterboxed onto exactly
// the canvas its depth map will steer. The InstantID and ControlNet strengths
// are re-applied here so the measured values cannot drift with the asset.
//
// On the vendored graph this touches node 1 (checkpoint), 2 (image), 7/8
// (prompts), 3 and 14 (bucket), 12 (ip_weight), 13 (depth strength) and
// 15/18 (seed).
func patchWorkflow(imageName, prompt string, b bucket, seed int64) (map[string]node, error) {
	var wf map[string]node
	if err := json.Unmarshal(workflowJSON, &wf); err != nil {
		return nil, fmt.Errorf("restyle: workflow template: %w", err)
	}
	subs := map[string]string{
		phPrompt:     prompt,
		phNegative:   negativePrompt,
		phImage:      imageName,
		phCheckpoint: Checkpoint,
	}
	seen := make(map[string]bool, len(subs))
	for _, n := range wf {
		for key, raw := range n.Inputs {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			if v, ok := subs[s]; ok {
				n.Inputs[key] = rawJSON(v)
				seen[s] = true
			}
		}
		switch n.ClassType {
		case "EmptyLatentImage", "EmptySD3LatentImage":
			n.Inputs["width"], n.Inputs["height"] = rawJSON(b.w), rawJSON(b.h)
			n.Inputs["batch_size"] = rawJSON(1)
		case "ImageResizeKJv2":
			n.Inputs["width"], n.Inputs["height"] = rawJSON(b.w), rawJSON(b.h)
		case "ApplyInstantIDAdvanced":
			n.Inputs["ip_weight"] = rawJSON(IPWeight)
		case "ControlNetApplyAdvanced":
			n.Inputs["strength"] = rawJSON(DepthStrength)
		}
		for _, key := range []string{"seed", "noise_seed"} {
			if _, ok := n.Inputs[key]; ok {
				n.Inputs[key] = rawJSON(seed)
			}
		}
	}
	for _, ph := range []string{phImage, phCheckpoint, phPrompt, phNegative} {
		if !seen[ph] {
			return nil, fmt.Errorf("restyle: %s is missing from the workflow template", ph)
		}
	}
	return wf, nil
}

// rawJSON encodes a string, an int or a float, none of which can fail.
func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Client talks to a ComfyUI server over its plain HTTP API.
type Client struct {
	// BaseURL is the ComfyUI root, e.g. http://127.0.0.1:8188.
	BaseURL string
	HTTP    *http.Client
	// JobTimeout bounds one variant; the context bounds the batch.
	JobTimeout time.Duration
	// PollInterval is how often /history is checked.
	PollInterval time.Duration
	// Rand seeds the per-variant generation seeds. Nil draws from a
	// crypto-seeded source.
	Rand *rand.Rand
	// Progress, when set, is called as a variant starts and again once its
	// file is on disk.
	Progress func(index, total int, s Style, done bool)

	clientID string
}

func New(baseURL string) *Client {
	var id [8]byte
	crand.Read(id[:]) // never fails
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		HTTP:         &http.Client{Timeout: 10 * time.Minute},
		JobTimeout:   DefaultJobTimeout,
		PollInterval: 2 * time.Second,
		clientID:     "promoclown-" + hex.EncodeToString(id[:]),
	}
}

// Error is a non-2xx answer from ComfyUI.
type Error struct {
	Status int
	Path   string
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("comfyui %s %d: %s", e.Path, e.Status, e.Body) }

// Result is one finished variant.
type Result struct {
	StyleID string `json:"style"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Seed    int64  `json:"seed"`
}

// Restyle renders one variant per style from the same source image and writes
// each to outDir. The jobs run one after another: ComfyUI has a single GPU, so
// queueing them together would only hide the failure of the later ones.
func (c *Client) Restyle(ctx context.Context, srcImagePath string, styles []Style, outDir string) ([]Result, error) {
	if len(styles) == 0 {
		return nil, fmt.Errorf("restyle: no styles given")
	}
	src, err := os.ReadFile(srcImagePath)
	if err != nil {
		return nil, fmt.Errorf("restyle: read source: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("restyle: output dir: %w", err)
	}
	rnd := c.Rand
	if rnd == nil {
		rnd = randomRand()
	}

	// One upload for the whole batch. ComfyUI's input folder is shared, so the
	// name has to be unique per run or two runs would race.
	var token [8]byte
	crand.Read(token[:]) // never fails
	upName := "promo_restyle_" + hex.EncodeToString(token[:]) + inputExt(srcImagePath)
	imageName, err := c.uploadImage(ctx, src, upName)
	if err != nil {
		return nil, fmt.Errorf("restyle: upload source: %w", err)
	}
	b := latentFor(src)

	results := make([]Result, 0, len(styles))
	for i, s := range styles {
		if c.Progress != nil {
			c.Progress(i, len(styles), s, false)
		}
		seed := rnd.Int64N(1 << 31)
		path, err := c.renderOne(ctx, imageName, b, s, seed, outDir, i)
		if err != nil {
			return results, fmt.Errorf("restyle %s: %w", s.ID, err)
		}
		r := Result{StyleID: s.ID, Label: s.Label, Path: path, Seed: seed}
		results = append(results, r)
		if c.Progress != nil {
			c.Progress(i, len(styles), s, true)
		}
	}
	return results, nil
}

func (c *Client) renderOne(ctx context.Context, imageName string, b bucket, s Style, seed int64, outDir string, index int) (string, error) {
	wf, err := patchWorkflow(imageName, s.Prompt(), b, seed)
	if err != nil {
		return "", err
	}
	jobCtx, cancel := context.WithTimeout(ctx, c.JobTimeout)
	defer cancel()

	promptID, err := c.queuePrompt(jobCtx, wf)
	if err != nil {
		return "", err
	}
	ref, err := c.waitForImage(jobCtx, promptID)
	if err != nil {
		return "", err
	}
	data, err := c.view(jobCtx, ref)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(ref.Filename)
	if ext == "" {
		ext = ".png"
	}
	path := filepath.Join(outDir, fmt.Sprintf("%02d-%s%s", index+1, s.ID, ext))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// inputExt keeps the source extension when ComfyUI can make sense of it;
// LoadImage reads the format from the bytes, the name is only a name.
func inputExt(path string) string {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return ext
	default:
		return ".png"
	}
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		body := strings.TrimSpace(string(data))
		if len(body) > 300 {
			body = body[:300] + "…"
		}
		return &Error{Status: resp.StatusCode, Path: req.URL.Path, Body: body}
	}
	switch out := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*out = data
		return nil
	default:
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", req.URL.Path, err)
		}
		return nil
	}
}

// uploadImage puts the source in ComfyUI's input folder and returns the name a
// LoadImage node takes (subfolder/name when nested).
func (c *Client) uploadImage(ctx context.Context, data []byte, filename string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.WriteField("overwrite", "true"); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/upload/image", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if out.Subfolder != "" {
		return out.Subfolder + "/" + out.Name, nil
	}
	return out.Name, nil
}

func (c *Client) queuePrompt(ctx context.Context, wf map[string]node) (string, error) {
	buf, err := json.Marshal(map[string]any{"prompt": wf, "client_id": c.clientID})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/prompt", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		PromptID   string          `json:"prompt_id"`
		NodeErrors json.RawMessage `json:"node_errors"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if s := string(out.NodeErrors); s != "" && s != "null" && s != "{}" {
		if len(s) > 300 {
			s = s[:300] + "…"
		}
		return "", fmt.Errorf("workflow rejected: %s", s)
	}
	if out.PromptID == "" {
		return "", fmt.Errorf("ComfyUI returned no prompt id")
	}
	return out.PromptID, nil
}

type imageRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

type historyEntry struct {
	Status struct {
		StatusStr string            `json:"status_str"`
		Messages  []json.RawMessage `json:"messages"`
	} `json:"status"`
	Outputs map[string]struct {
		Images []imageRef `json:"images"`
	} `json:"outputs"`
}

// waitForImage polls /history until the job has an entry, then returns its
// first saved output.
func (c *Client) waitForImage(ctx context.Context, promptID string) (imageRef, error) {
	ticker := time.NewTicker(c.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return imageRef{}, fmt.Errorf("waiting for prompt %s: %w", promptID, ctx.Err())
		case <-ticker.C:
		}
		hist, err := c.history(ctx, promptID)
		if err != nil || hist == nil {
			// A hiccup while the GPU is busy is not a failed job.
			continue
		}
		if hist.Status.StatusStr == "error" {
			return imageRef{}, fmt.Errorf("%s", executionError(hist.Status.Messages))
		}
		for _, out := range hist.Outputs {
			for _, img := range out.Images {
				if img.Type != "temp" {
					return img, nil
				}
			}
		}
		return imageRef{}, fmt.Errorf("prompt %s produced no output image", promptID)
	}
}

func (c *Client) history(ctx context.Context, promptID string) (*historyEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return nil, err
	}
	var out map[string]historyEntry
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	entry, ok := out[promptID]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (c *Client) view(ctx context.Context, ref imageRef) ([]byte, error) {
	typ := ref.Type
	if typ == "" {
		typ = "output"
	}
	q := url.Values{"filename": {ref.Filename}, "subfolder": {ref.Subfolder}, "type": {typ}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var data []byte
	return data, c.do(req, &data)
}

// executionError digs the failing node's own exception out of the history
// status: for this workflow that is the difference between "generation failed"
// and "no face found in the photo", which is the one thing a caller can act on.
func executionError(messages []json.RawMessage) string {
	for _, raw := range messages {
		var pair []json.RawMessage
		if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
			continue
		}
		var kind string
		if json.Unmarshal(pair[0], &kind) != nil || kind != "execution_error" {
			continue
		}
		var data struct {
			ExceptionMessage string `json:"exception_message"`
		}
		if json.Unmarshal(pair[1], &data) != nil {
			continue
		}
		if line, _, _ := strings.Cut(strings.TrimSpace(data.ExceptionMessage), "\n"); line != "" {
			if len(line) > 160 {
				line = line[:160]
			}
			return "ComfyUI: " + line
		}
	}
	return "generation failed on the ComfyUI side"
}

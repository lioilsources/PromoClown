// Package model holds the JSON shapes shared by promo-api, the promo CLI and
// the monitor CLIs. Timestamps are RFC 3339 UTC strings.
package model

const (
	PlatformBluesky = "bluesky"
	PlatformX       = "x"
	PlatformYouTube = "youtube"
	PlatformReddit  = "reddit"

	StatusDraft     = "draft"
	StatusApproved  = "approved"
	StatusScheduled = "scheduled"
	StatusPublished = "published"
	StatusRejected  = "rejected"
	StatusFailed    = "failed"

	StoreAppStore   = "appstore"
	StoreGooglePlay = "googleplay"
)

// Platforms that promo-api can draft for.
var Platforms = []string{PlatformBluesky, PlatformX, PlatformYouTube, PlatformReddit}

// Kinds of post.
var Kinds = []string{"post", "short", "video", "reply"}

type Project struct {
	Slug            string   `json:"slug" yaml:"slug"`
	Name            string   `json:"name" yaml:"name"`
	Tagline         string   `json:"tagline,omitempty" yaml:"tagline"`
	Audience        string   `json:"audience,omitempty" yaml:"audience"`
	Tags            []string `json:"tags,omitempty" yaml:"tags"`
	Hooks           []string `json:"hooks,omitempty" yaml:"hooks"`
	ForbiddenClaims []string `json:"forbidden_claims,omitempty" yaml:"forbidden_claims"`
	WebsiteURL      string   `json:"website_url,omitempty" yaml:"website_url"`
	StoreIOSURL     string   `json:"store_ios_url,omitempty" yaml:"store_ios_url"`
	StoreAndroidURL string   `json:"store_android_url,omitempty" yaml:"store_android_url"`
	IOSAppID        string   `json:"ios_app_id,omitempty" yaml:"ios_app_id"`
	AndroidPackage  string   `json:"android_package,omitempty" yaml:"android_package"`
	AssetsDir       string   `json:"assets_dir,omitempty" yaml:"assets_dir"`
	// PostizAccounts maps a platform to the Postiz channel that publishes it
	// ("x": "wakeupm_sfx"); a platform left out uses POSTIZ_INTEGRATIONS.
	PostizAccounts map[string]string `json:"postiz_accounts,omitempty" yaml:"postiz_accounts"`
	// DailyCap is how many posts this project may put on one account in a day;
	// zero means the default of one.
	DailyCap int `json:"daily_cap,omitempty" yaml:"daily_cap"`
	// MinDaysBetween is the gap between two of this project's posts on the same
	// platform. It is a pointer because zero is a real answer — tributes credit
	// a different artist every time and need no gap — and has to be told apart
	// from a config file that does not mention it at all, which means seven.
	MinDaysBetween *int   `json:"min_days_between,omitempty" yaml:"min_days_between"`
	Status         string `json:"status,omitempty" yaml:"status"`
	UpdatedAt      string `json:"updated_at,omitempty" yaml:"-"`
}

// Account is the Postiz channel this project posts to on the platform, empty
// when it uses the platform's default channel.
func (p Project) Account(platform string) string { return p.PostizAccounts[platform] }

// Links returns the project's public links, store links first.
func (p Project) Links() []string {
	var out []string
	for _, u := range []string{p.StoreIOSURL, p.StoreAndroidURL, p.WebsiteURL} {
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}

type Post struct {
	ID           int64    `json:"id"`
	Project      string   `json:"project"`
	Platform     string   `json:"platform"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title,omitempty"`
	Text         string   `json:"text"`
	MediaPaths   []string `json:"media_paths,omitempty"`
	Account      string   `json:"account,omitempty"`
	ReplyToURL   string   `json:"reply_to_url,omitempty"`
	Status       string   `json:"status"`
	Revision     int64    `json:"revision"`
	CreatedBy    string   `json:"created_by"`
	PostizPostID string   `json:"postiz_post_id,omitempty"`
	ReleaseURL   string   `json:"release_url,omitempty"`
	Error        string   `json:"error,omitempty"`
	ScheduledAt  string   `json:"scheduled_at,omitempty"`
	PublishedAt  string   `json:"published_at,omitempty"`
	ApprovedAt   string   `json:"approved_at,omitempty"`
	ApprovedBy   string   `json:"approved_by,omitempty"`
	CreatedAt    string   `json:"created_at"`
	// Revision of the last Telegram preview; approving by text uses it.
	NotifiedRevision int64 `json:"notified_revision,omitempty"`
	TelegramMsgID    int64 `json:"-"`
}

type PostEvent struct {
	ID        int64  `json:"id"`
	PostID    int64  `json:"post_id"`
	Action    string `json:"action"`
	Actor     string `json:"actor"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}

type DraftRequest struct {
	Project    string   `json:"project"`
	Platform   string   `json:"platform"`
	Kind       string   `json:"kind,omitempty"`
	Title      string   `json:"title,omitempty"`
	Text       string   `json:"text"`
	MediaPaths []string `json:"media_paths,omitempty"`
	ReplyToURL string   `json:"reply_to_url,omitempty"`
}

// PostResult is returned by every call that creates or changes a post.
type PostResult struct {
	Post     Post     `json:"post"`
	Warnings []string `json:"warnings,omitempty"`
}

type ApproveRequest struct {
	// Revision the approver saw. A mismatch means the text changed since.
	Revision int64 `json:"revision"`
	// RFC 3339; empty picks the next slot allowed by the frequency rules.
	ScheduledAt string `json:"scheduled_at,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

type EditRequest struct {
	Text       *string   `json:"text,omitempty"`
	Title      *string   `json:"title,omitempty"`
	MediaPaths *[]string `json:"media_paths,omitempty"`
	Actor      string    `json:"actor,omitempty"`
}

type RejectRequest struct {
	Reason string `json:"reason,omitempty"`
	Actor  string `json:"actor,omitempty"`
}

type RetryRequest struct {
	ScheduledAt string `json:"scheduled_at,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

// PublishedRequest records a post that went out outside Postiz (Reddit replies
// are posted by hand) or whose status arrived from a webhook.
type PublishedRequest struct {
	URL   string `json:"url,omitempty"`
	Actor string `json:"actor,omitempty"`
}

// Tribute is a picture somebody else made, queued to be restyled and posted
// with a credit to its author. The queue is filled from Telegram during the
// day and drained by promo-tribute at night, when the GPU is free.
type Tribute struct {
	ID          int64    `json:"id"`
	Project     string   `json:"project"`
	Credit      string   `json:"credit"` // "@handle" the post thanks
	SourcePath  string   `json:"source_path"`
	Note        string   `json:"note,omitempty"`
	Status      string   `json:"status"`
	PostID      int64    `json:"post_id,omitempty"`
	Styles      []string `json:"styles,omitempty"`
	Error       string   `json:"error,omitempty"`
	SubmittedBy string   `json:"submitted_by,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

type TributeRequest struct {
	Project    string `json:"project"`
	Credit     string `json:"credit"`
	SourcePath string `json:"source_path"`
	Note       string `json:"note,omitempty"`
	Actor      string `json:"actor,omitempty"`
}

// TributeDoneRequest reports the draft a tribute turned into.
type TributeDoneRequest struct {
	PostID int64    `json:"post_id"`
	Styles []string `json:"styles,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type Mention struct {
	ID         int64  `json:"id,omitempty"`
	Platform   string `json:"platform"`
	Kind       string `json:"kind,omitempty"`
	ExternalID string `json:"external_id"`
	URL        string `json:"url,omitempty"`
	Author     string `json:"author,omitempty"`
	Text       string `json:"text,omitempty"`
	Context    string `json:"context,omitempty"`
	Project    string `json:"project,omitempty"`
	PostedAt   string `json:"posted_at,omitempty"`
	SeenAt     string `json:"seen_at,omitempty"`
	Handled    bool   `json:"handled,omitempty"`
}

type Review struct {
	ID         int64  `json:"id,omitempty"`
	Store      string `json:"store"`
	AppID      string `json:"app_id"`
	Project    string `json:"project,omitempty"`
	ExternalID string `json:"external_id"`
	Rating     int    `json:"rating"`
	Title      string `json:"title,omitempty"`
	Text       string `json:"text,omitempty"`
	Author     string `json:"author,omitempty"`
	Version    string `json:"version,omitempty"`
	Territory  string `json:"territory,omitempty"`
	PostedAt   string `json:"posted_at,omitempty"`
	SeenAt     string `json:"seen_at,omitempty"`
	Handled    bool   `json:"handled,omitempty"`
}

type ImportResult struct {
	Received int     `json:"received"`
	Inserted int     `json:"inserted"`
	IDs      []int64 `json:"ids,omitempty"`
}

type Asset struct {
	Path string `json:"path"` // relative to the assets root, usable as media_path
	Kind string `json:"kind"` // image | video | other
	Size int64  `json:"size"`
}

type Digest struct {
	Since     string    `json:"since"`
	Mentions  []Mention `json:"mentions"`
	Reviews   []Review  `json:"reviews"`
	Drafts    []Post    `json:"drafts"`
	Scheduled []Post    `json:"scheduled"`
	Published []Post    `json:"published"`
	Failed    []Post    `json:"failed"`
}

// Empty reports whether the digest has nothing worth telling anyone.
func (d Digest) Empty() bool {
	return len(d.Mentions) == 0 && len(d.Reviews) == 0 && len(d.Published) == 0 && len(d.Failed) == 0
}

type Error struct {
	Error    string   `json:"error"`
	Problems []string `json:"problems,omitempty"`
}

type IDsRequest struct {
	IDs []int64 `json:"ids"`
}

type CountResult struct {
	Updated int64 `json:"updated"`
}

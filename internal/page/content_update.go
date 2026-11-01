package page

// ContentUpdateKind names what a content.updated signal of a Page reports.
type ContentUpdateKind uint8

const (
	// SettingsUpdate reports changed Page settings; Fields names them
	// ("slug", "showTitle", "accessPolicy").
	SettingsUpdate ContentUpdateKind = iota + 1
	// StateUpdate reports a changed lifecycle status; Fields names the
	// state paths ("state.status", "state.published_at").
	StateUpdate
	// FeaturedImageUpdate reports a changed featured image.
	FeaturedImageUpdate
	// LocaleUpdate reports a changed locale of the content document.
	LocaleUpdate
	// LayoutUpdate reports a document layout a collaboration room saved,
	// at Locale.DocumentRevision by Locale.Contributors.
	LayoutUpdate
)

// UpdateSource names the surface a locale change came from.
type UpdateSource uint8

const (
	SourceManage UpdateSource = iota + 1
	SourceCollaboration
	SourceAI
)

// ContentUpdate is one content.updated signal the Outbox step schedules.
type ContentUpdate struct {
	PageID string
	Kind   ContentUpdateKind
	// Fields names what a SettingsUpdate or StateUpdate changed.
	Fields []string
	// Locale describes a LocaleUpdate, and the revision and contributors
	// of a LayoutUpdate.
	Locale LocaleUpdateChange
}

// LocaleUpdateChange is the exact locale fence the collaboration runtime
// consumes. Source writes advance the shared document revision; target
// writes carry only their opaque target revision and a target deletion
// carries LocaleDeleted.
type LocaleUpdateChange struct {
	// Fields names what changed: "title", "summary", "content",
	// "documentLayout" or "sourceLocale".
	Fields           []string
	DocumentRevision string
	Contributors     []string
	Source           UpdateSource
	Locale           string
	LocaleDeleted    bool
	// TargetRevision is the new target revision of a target-locale change;
	// nil for a source change or a deleted locale.
	TargetRevision *string
	// DocumentStateChanged is set when the shared document revision
	// advanced, which only a source-locale change does.
	DocumentStateChanged bool
}

package label

// ContentUpdateKind names what a content.updated signal of a Label reports.
type ContentUpdateKind uint8

const (
	// MediaUpdate reports a changed logo.
	MediaUpdate ContentUpdateKind = iota + 1
	// StateUpdate reports a changed lifecycle status.
	StateUpdate
	// LocaleUpdate reports a changed locale of the content document.
	LocaleUpdate
)

// UpdateSource names the surface a locale change came from.
type UpdateSource uint8

const (
	SourceCollaboration UpdateSource = iota + 1
	SourceAI
)

// ContentUpdate is one content.updated signal the Outbox step schedules.
type ContentUpdate struct {
	LabelID string
	Kind    ContentUpdateKind
	// Locale describes a LocaleUpdate.
	Locale LocaleChange
}

// LocaleChange is the exact locale fence the collaboration runtime
// consumes. Source writes advance the shared document revision; target
// writes carry only their opaque target revision and a target deletion
// carries LocaleDeleted.
type LocaleChange struct {
	// Fields names what changed: "title", "content" or a configuration
	// field.
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

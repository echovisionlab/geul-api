package work

// ContentUpdateKind names what a content.updated signal of a Work reports.
type ContentUpdateKind uint8

const (
	// SettingsUpdate reports changed settings; Fields names them in the
	// command's words (slug, type, metadata, ..., mapPlaceId, isPresent).
	SettingsUpdate ContentUpdateKind = iota + 1
	// StateUpdate reports a changed lifecycle; Fields names the state
	// paths.
	StateUpdate
	// MediaUpdate reports a changed featured image.
	MediaUpdate
	// LocaleUpdate reports a changed locale of the content document.
	LocaleUpdate
	// InteractiveUpdate reports an AI document mutation for an interactive
	// caller (contentsignal.PublishInteractive).
	InteractiveUpdate
)

// UpdateSource names the surface a locale change came from.
type UpdateSource uint8

const (
	SourceManage UpdateSource = iota + 1
	SourceCollaboration
)

// ContentUpdate is one content.updated signal the Outbox step schedules.
type ContentUpdate struct {
	WorkID string
	Kind   ContentUpdateKind
	Fields []string
	// Locale describes a LocaleUpdate.
	Locale LocaleChange
	// Interactive describes an InteractiveUpdate.
	Interactive InteractiveChange
}

// LocaleChange is the exact locale fence the collaboration runtime
// consumes. The shared content document revision advances only for
// source-locale mutations; target-only changes carry their opaque target
// revision and leave DocumentStateChanged false.
type LocaleChange struct {
	Fields               []string
	DocumentRevision     string
	Contributors         []string
	Source               UpdateSource
	Locale               string
	TargetRevision       *string
	DocumentStateChanged bool
}

// InteractiveChange is an AI document mutation's signal.
type InteractiveChange struct {
	Locale           string
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
	LocaleDeleted    bool
	ContributorID    string
}

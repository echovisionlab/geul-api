package artist

// ContentUpdateKind names what a content.updated signal of an Artist
// reports.
type ContentUpdateKind uint8

const (
	// MediaUpdate reports changed images.
	MediaUpdate ContentUpdateKind = iota + 1
	// StateUpdate reports a changed lifecycle; StatePaths names the
	// fields.
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
	ArtistID   string
	Kind       ContentUpdateKind
	StatePaths []string
	// Locale describes a LocaleUpdate.
	Locale LocaleChange
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
	LocaleDeleted        bool
	TargetRevision       *string
	DocumentStateChanged bool
}

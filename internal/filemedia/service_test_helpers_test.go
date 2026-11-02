//go:build integration

package filemedia

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/identitystate"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
)

var errUnusedTestDependency = errors.New("test did not configure this File dependency")

// newTestService builds the File use cases through NewService. Ports left
// unset in deps get stand-ins that fail if a test reaches them, and a
// recording audit appender.
func newTestService(deps Deps, db *gorm.DB, ports nativeFilePorts) *Service {
	if deps.Fence == nil {
		deps.Fence = identitystate.PostgresFence{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.LegalRoutes == nil {
		deps.LegalRoutes = unusedFilePorts{}
	}
	if deps.MemberSummaries == nil {
		deps.MemberSummaries = unusedFilePorts{}
	}
	if ports.Posts == nil {
		ports.Posts = unusedFilePorts{}
	}
	if ports.Pages == nil {
		ports.Pages = unusedFilePorts{}
	}
	if ports.Works == nil {
		ports.Works = unusedFilePorts{}
	}
	if ports.Releases == nil {
		ports.Releases = unusedFilePorts{}
	}
	if ports.Events == nil {
		ports.Events = unusedProgramEventAttachment{}
	}
	if ports.Tracks == nil {
		ports.Tracks = unusedFilePorts{}
	}
	native := withNativeFileUnits(&Service{asyncPublisher: deps.AsyncPublisher, spiceDB: deps.SpiceDB}, db, ports)
	if deps.IngestLocks == nil {
		deps.IngestLocks = native.ingestLocks
	}
	if deps.FaviconCommits == nil {
		deps.FaviconCommits = native.faviconCommits
	}
	if deps.Units == nil {
		deps.Units = native.units
	} else {
		deps.Units = nativeFileUnits{deps.Units, db, testFilePorts(native), ports.Transcoder}
	}
	service, err := NewService(deps)
	if err != nil {
		panic(err)
	}
	return service
}

// unusedFilePorts answers every File port whose method set it shares with
// errUnusedTestDependency.
type unusedFilePorts struct{}

func (unusedFilePorts) RouteID(string) string { return "" }

func (unusedFilePorts) RequireView(context.Context, string) error { return errUnusedTestDependency }

func (unusedFilePorts) RequireEdit(context.Context, string) error { return errUnusedTestDependency }

func (unusedFilePorts) RequireExists(context.Context, string) error { return errUnusedTestDependency }

func (unusedFilePorts) RequireLockedView(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedFilePorts) RequireLockedEdit(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedFilePorts) LockExistsWithDB(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedFilePorts) AttachOriginalWithDB(context.Context, *gorm.DB, TrackOriginalAudioInput) (TrackOriginalAudioAttachment, error) {
	return TrackOriginalAudioAttachment{}, errUnusedTestDependency
}

func (unusedFilePorts) Load(context.Context, []string) (map[string]MemberSummary, error) {
	return nil, errUnusedTestDependency
}

type unusedProgramEventAttachment struct{}

func (unusedProgramEventAttachment) RequireView(context.Context, *auth.SpiceDBClient, string) error {
	return errUnusedTestDependency
}

func (unusedProgramEventAttachment) RequireEdit(context.Context, *auth.SpiceDBClient, string) error {
	return errUnusedTestDependency
}

func (unusedProgramEventAttachment) RequireLockedView(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	return errUnusedTestDependency
}

func (unusedProgramEventAttachment) RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	return errUnusedTestDependency
}

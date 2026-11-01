//go:build integration

package filemedia

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/mutation"

	"github.com/echovisionlab/geul-api/internal/identitystate"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

var errUnusedTestDependency = errors.New("test did not configure this File dependency")

// newTestService builds the File use cases through NewService. Ports left
// unset in deps get stand-ins that fail if a test reaches them, and a
// recording audit appender.
func newTestService(deps Deps) *Service {
	if deps.Fence == nil {
		deps.Fence = identitystate.PostgresFence{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}

	if deps.LegalRoutes == nil {
		deps.LegalRoutes = unusedFilePorts{}
	}
	if deps.PostAccess == nil {
		deps.PostAccess = unusedFilePorts{}
	}
	if deps.PagePolicyAccess == nil {
		deps.PagePolicyAccess = unusedFilePorts{}
	}
	if deps.WorkAttachment == nil {
		deps.WorkAttachment = unusedFilePorts{}
	}
	if deps.WorkPolicyAccess == nil {
		deps.WorkPolicyAccess = unusedFilePorts{}
	}
	if deps.ProgramEventAttachment == nil {
		deps.ProgramEventAttachment = unusedProgramEventAttachment{}
	}
	if deps.TrackAttachment == nil {
		deps.TrackAttachment = unusedFilePorts{}
	}
	if deps.ReleasePolicyAccess == nil {
		deps.ReleasePolicyAccess = unusedFilePorts{}
	}

	if deps.MemberSummaries == nil {
		deps.MemberSummaries = unusedFilePorts{}
	}
	if deps.Units == nil {
		deps.Units = mutation.Postgres(deps.DB, func(tx *gorm.DB) Store {
			return testFileStore(&Service{publisher: deps.Transcoder, trackAttachment: deps.TrackAttachment, asyncPublisher: deps.AsyncPublisher, postAccess: deps.PostAccess, pagePolicyAccess: deps.PagePolicyAccess, workPolicyAccess: deps.WorkPolicyAccess, programEventAttachment: deps.ProgramEventAttachment, releasePolicyAccess: deps.ReleasePolicyAccess, spiceDB: deps.SpiceDB}, tx)
		})
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

func (unusedFilePorts) Load(context.Context, []string) (map[string]*commonv1.MemberSummary, error) {
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

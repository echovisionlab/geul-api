package account

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errUnusedTestDependency = errors.New("test did not configure this Account dependency")

// newTestService builds the Account use cases through NewService. Fields
// left unset in deps get stand-ins that fail if a test reaches them, and a
// recording audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Identity == nil {
		deps.Identity = unusedIdentity{}
	}
	if deps.Publisher == nil {
		deps.Publisher = unusedPublisher{}
	}
	if deps.Newsletter == nil {
		deps.Newsletter = unusedNewsletter{}
	}
	if deps.MemberDeletion == nil {
		deps.MemberDeletion = unusedMemberDeletion{}
	}
	if deps.MemberEmails == nil {
		deps.MemberEmails = unusedMemberEmails{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

// requestDeletionFor runs the principal's deletion request for an exact
// Member and Identity pair.
func requestDeletionFor(ctx context.Context, service *Service, memberID, identityID string) (*AccountDeletionRequestResult, error) {
	return service.requestDeletion(ctx, &auth.UserInfo{
		MemberID: auth.MemberID(memberID), IdentityID: auth.IdentityID(identityID),
	})
}

type unusedIdentity struct{ accountIdentityManager }

type unusedPublisher struct{}

func (unusedPublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return errUnusedTestDependency
}

func (unusedPublisher) PublishUserDeleteIdentity(context.Context, *managev1.UserDeleteIdentityCommand) error {
	return errUnusedTestDependency
}

func (unusedPublisher) PublishUserDeleteAvatar(context.Context, *managev1.UserDeleteAvatarCommand) error {
	return errUnusedTestDependency
}

func (unusedPublisher) PublishUserDeleteIdentityWithExecutor(context.Context, eventpkg.DBTX, *managev1.UserDeleteIdentityCommand) error {
	return errUnusedTestDependency
}

type unusedNewsletter struct{}

func (unusedNewsletter) Mutate(context.Context, *gorm.DB, string, bool) (bool, error) {
	return false, errUnusedTestDependency
}

func (unusedNewsletter) State(context.Context, *gorm.DB, string) (*managev1.NewsletterSubscriptionState, error) {
	return nil, errUnusedTestDependency
}

func (unusedNewsletter) AuditStates(bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	return "", ""
}

type unusedMemberDeletion struct{ MemberDeletionLifecycle }

type unusedMemberEmails struct{ MemberEmailProjection }

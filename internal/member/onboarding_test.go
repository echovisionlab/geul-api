package member_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// pendingFixture is a fixture whose principal is not onboarded yet, with a
// deliverable welcome recipient.
func pendingFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	principal, _ := f.store.Member(f.memberID)
	principal.Onboarded = false
	principal.Nickname = f.memberID
	locale := "ko-KR"
	principal.PreferredLocale = &locale
	f.store.SeedMember(principal)
	f.store.SeedWelcomeDelivery(f.identityID, member.WelcomeDelivery{Recipient: "member@example.test", LinkedMemberID: f.memberID})
	return f
}

func TestCompleteOnboardingEnqueuesTheWelcomeEmailWithTheTransition(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)

	summary, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "  Chosen  "})

	require.NoError(t, err)
	require.Equal(t, "Chosen", summary.Nickname)
	stored, _ := f.store.Member(f.memberID)
	require.True(t, stored.Onboarded)
	welcome := f.store.WelcomeEmails()
	require.Len(t, welcome, 1)
	require.Equal(t, member.WelcomeEmail{
		CommandID: "welcome:" + f.identityID, IdentityID: f.identityID, Recipient: "member@example.test",
		Nickname: "Chosen", LoginURL: "https://www.example.test/login", Locale: welcome[0].Locale,
	}, welcome[0])
	require.NotNil(t, welcome[0].Locale)
	records := f.memberUpdates()
	require.Len(t, records, 1)
	require.Equal(t, "Chosen", records[0].Nickname)
	require.Equal(t, []mutation.Step{
		mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepOutbox, mutation.StepCommit,
	}, f.units.Steps())
}

func TestCompleteOnboardingSameNicknameRetryIsIdempotent(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)
	_, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.NoError(t, err)

	summary, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.NoError(t, err)
	require.Equal(t, "Chosen", summary.Nickname)
	require.Len(t, f.store.WelcomeEmails(), 1)
	require.Len(t, f.memberUpdates(), 1)

	_, err = f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Other"})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "member onboarding is already complete")
}

func TestCompleteOnboardingTakenNicknameAnswersAlreadyExists(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)
	f.seedMember("Chosen")

	_, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})

	require.Equal(t, connect.CodeAlreadyExists, errs.Wrap(err).Code())
	stored, _ := f.store.Member(f.memberID)
	require.False(t, stored.Onboarded)
	require.Empty(t, f.store.WelcomeEmails())
}

func TestCompleteOnboardingWelcomeEnqueueFailureRollsBackThenRetrySends(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)
	queueDown := errors.New("queue unavailable")
	f.store.Fail("EnqueueWelcomeEmail", queueDown)

	_, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.ErrorIs(t, err, queueDown)
	require.ErrorContains(t, err, "publish welcome email command")
	stored, _ := f.store.Member(f.memberID)
	require.False(t, stored.Onboarded)
	require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])

	f.store.Fail("EnqueueWelcomeEmail", nil)
	_, err = f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.NoError(t, err)
	require.Len(t, f.store.WelcomeEmails(), 1)
}

func TestCompleteOnboardingRecipientFailureAnswersUnavailableAndSkipsAMismatch(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)
	f.store.Fail("WelcomeDelivery", errors.New("kratos unavailable"))
	_, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.Equal(t, connect.CodeUnavailable, errs.Wrap(err).Code())

	f.store.Fail("WelcomeDelivery", nil)
	f.store.SeedWelcomeDelivery(f.identityID, member.WelcomeDelivery{Reason: "email_unverified"})
	_, err = f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.NoError(t, err)
	require.Empty(t, f.store.WelcomeEmails())
	stored, _ := f.store.Member(f.memberID)
	require.True(t, stored.Onboarded)
}

func TestCompleteOnboardingAuditFailureRollsBackTheTransition(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)

	_, err := f.newService(t, &audittest.Recorder{Err: errAuditUnavailable}).CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})

	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	stored, _ := f.store.Member(f.memberID)
	require.False(t, stored.Onboarded)
	require.Equal(t, f.memberID, stored.Nickname)
	require.Empty(t, f.store.WelcomeEmails())
}

func TestCompleteOnboardingAttributesTheRecordToTheMember(t *testing.T) {
	t.Parallel()
	f := pendingFixture(t)
	_, err := f.service.CompleteOnboarding(f.ctx(t), member.CompleteOnboardingCommand{Nickname: "Chosen"})
	require.NoError(t, err)
	record := f.memberUpdates()[0]
	require.Equal(t, sharedtelemetry.ActorKindMember, record.Kind)
	require.Equal(t, f.memberID, record.MemberID)
}

//go:build integration

package member

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type onboardingMemberFixture struct {
	IdentityID string
	MemberID   string
	Email      string
}

type recordingOnboardingWelcomePublisher struct {
	mu   sync.Mutex
	err  error
	jobs []*managev1.SendEmailEvent
}

func (p *recordingOnboardingWelcomePublisher) PublishSendEmail(
	_ context.Context,
	job *managev1.SendEmailEvent,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append(p.jobs, job)
	return p.err
}

func (p *recordingOnboardingWelcomePublisher) snapshot() []*managev1.SendEmailEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*managev1.SendEmailEvent(nil), p.jobs...)
}

func createOnboardingMember(t *testing.T, db *gorm.DB, email string) onboardingMemberFixture {
	t.Helper()
	fixture := onboardingMemberFixture{IdentityID: uuid.NewString(), MemberID: uuid.NewString(), Email: email}
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: fixture.IdentityID, Email: fixture.Email, Name: fixture.MemberID,
	})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE kratos.identities SET external_id = ?::text WHERE id = ?::uuid`, fixture.MemberID, fixture.IdentityID,
		).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, fixture.IdentityID).Error
	}))
	require.NoError(t, db.Exec(`
		INSERT INTO member (
			id, account_identity_id, nickname, onboarded,
			primary_email, available_emails, social_links
		) VALUES (?::uuid, ?::uuid, ?, FALSE, ?, ARRAY[?::text], '{}'::jsonb)
	`, fixture.MemberID, fixture.IdentityID, fixture.MemberID, fixture.Email, fixture.Email).Error)
	return fixture
}

func onboardingContext(t *testing.T, fixture onboardingMemberFixture) *auth.UserInfo {
	t.Helper()
	return &auth.UserInfo{
		IdentityID:    auth.IdentityID(fixture.IdentityID),
		MemberID:      auth.MemberID(fixture.MemberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     false,
	}
}

func withOnboardingRequestContext(t *testing.T, fixture onboardingMemberFixture) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("203.0.113.10")
	require.NoError(t, err)
	return auth.WithUser(
		sharedtelemetry.WithRequestContext(t.Context(), requestContext),
		onboardingContext(t, fixture),
	)
}

func onboardingIdentity(fixture onboardingMemberFixture) *auth.Identity {
	return &auth.Identity{
		ID:         fixture.IdentityID,
		ExternalID: fixture.MemberID,
		State:      auth.KratosStateActive,
		Traits:     structured.Fields{"email": fixture.Email},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{fixture.Email}},
		},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Via: "email", Value: fixture.Email, Verified: true,
		}},
	}
}

func onboardingService(
	db *gorm.DB,
	fixture onboardingMemberFixture,
	publisher EmailCommandPublisher,
) *MemberService {
	return &MemberService{
		db:                     db,
		identity:               &fakeIdentityManager{identity: onboardingIdentity(fixture)},
		siteOrigin:             "https://www.example.test",
		welcomeEmailPublisher:  publisher,
		accountEmailProjection: integrationAccountEmailProjection{},
		auditWriter:            apitelemetry.NewDurableWriter(db),
	}
}

type failingOnboardingAuditWriter struct{}

func (failingOnboardingAuditWriter) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit persistence unavailable")
}

func TestCompleteMyOnboardingConcurrentNicknameClaimHasOneWinner(t *testing.T) {
	stack := newConcurrentServiceIntegrationPostgres(t)
	first := createOnboardingMember(t, stack.DB, "first@example.test")
	second := createOnboardingMember(t, stack.DB, "second@example.test")
	publisher := &recordingOnboardingWelcomePublisher{}

	type result struct {
		memberID string
		err      error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, fixture := range []onboardingMemberFixture{first, second} {
		group.Add(1)
		go func(fixture onboardingMemberFixture) {
			defer group.Done()
			<-start
			ctx := withOnboardingRequestContext(t, fixture)
			service := onboardingService(stack.DB, fixture, publisher)
			_, err := service.CompleteMyOnboarding(ctx, connect.NewRequest(&managev1.CompleteMyOnboardingRequest{Nickname: "SameNickname"}))
			results <- result{memberID: fixture.MemberID, err: err}
		}(fixture)
	}
	close(start)
	group.Wait()
	close(results)

	succeeded := 0
	conflicted := 0
	winnerMemberID := ""
	for result := range results {
		if result.err == nil {
			succeeded++
			winnerMemberID = result.memberID
			continue
		}
		switch connect.CodeOf(result.err) {
		case connect.CodeAlreadyExists:
			conflicted++
		default:
			t.Fatalf("member %s result = %v", result.memberID, result.err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, conflicted)

	var members []model.Member
	require.NoError(t, stack.DB.Where("id IN ?", []string{first.MemberID, second.MemberID}).Find(&members).Error)
	onboarded := 0
	missing := 0
	for _, member := range members {
		if member.Onboarded {
			onboarded++
			require.Equal(t, "SameNickname", member.Nickname)
		} else {
			missing++
			require.Equal(t, member.ID, member.Nickname)
		}
	}
	require.Equal(t, 1, onboarded)
	require.Equal(t, 1, missing)
	jobs := publisher.snapshot()
	require.Len(t, jobs, 1)
	require.Equal(t, "SameNickname", jobs[0].GetTemplateData()["name"])
	require.Contains(t, []string{
		"welcome:" + first.IdentityID,
		"welcome:" + second.IdentityID,
	}, jobs[0].GetMessageId())

	var audit struct {
		ActorMemberID string         `gorm:"column:actor_member_id"`
		TargetID      string         `gorm:"column:target_id"`
		Nickname      string         `gorm:"column:nickname"`
		ChangedFields pq.StringArray `gorm:"type:text[]"`
		RequestID     string         `gorm:"column:request_id"`
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT actor_member_id::text AS actor_member_id, target_id,
		       attributes->>'nickname' AS nickname,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       request_id::text AS request_id
		FROM public.domain_audit
		WHERE action = ?
	`, sharedtelemetry.AuditMemberUpdated).Take(&audit).Error)
	require.Equal(t, winnerMemberID, audit.ActorMemberID)
	require.Equal(t, winnerMemberID, audit.TargetID)
	require.Equal(t, "SameNickname", audit.Nickname)
	require.Equal(t, pq.StringArray{"nickname", "onboarded"}, audit.ChangedFields)
	require.NotEmpty(t, audit.RequestID)
}

func TestCompleteMyOnboardingSameNicknameRetryIsIdempotent(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createOnboardingMember(t, db, "member@example.test")
	publisher := &recordingOnboardingWelcomePublisher{}
	service := onboardingService(db, fixture, publisher)
	ctx := withOnboardingRequestContext(t, fixture)
	request := connect.NewRequest(&managev1.CompleteMyOnboardingRequest{Nickname: "Member"})

	first, err := service.CompleteMyOnboarding(ctx, request)
	require.NoError(t, err)
	second, err := service.CompleteMyOnboarding(ctx, request)
	require.NoError(t, err)
	require.True(t, first.Msg.Onboarded)
	require.True(t, second.Msg.Onboarded)
	require.Equal(t, "Member", first.Msg.Member.GetNickname())
	require.Equal(t, "Member", second.Msg.Member.GetNickname())

	_, err = service.CompleteMyOnboarding(ctx, connect.NewRequest(&managev1.CompleteMyOnboardingRequest{Nickname: "Different"}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	jobs := publisher.snapshot()
	require.Len(t, jobs, 1, "idempotent replay and conflicting replay must not republish")
	require.Equal(t, "welcome:"+fixture.IdentityID, jobs[0].GetMessageId())
	require.Equal(t, fixture.Email, jobs[0].GetRecipient())
	require.Equal(t, "Member", jobs[0].GetTemplateData()["name"])
	require.Equal(t, "https://www.example.test/login", jobs[0].GetTemplateData()["login_url"])

	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action = ? AND target_id = ?", sharedtelemetry.AuditMemberUpdated, fixture.MemberID).
		Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestCompleteMyOnboardingPublishFailureDoesNotRollbackTransition(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createOnboardingMember(t, db, "publish-failure@example.test")
	publisher := &recordingOnboardingWelcomePublisher{err: errors.New("broker unavailable")}
	service := onboardingService(db, fixture, publisher)
	ctx := withOnboardingRequestContext(t, fixture)

	response, err := service.CompleteMyOnboarding(
		ctx,
		connect.NewRequest(&managev1.CompleteMyOnboardingRequest{Nickname: "CommittedMember"}),
	)

	require.NoError(t, err)
	require.True(t, response.Msg.Onboarded)
	require.Equal(t, "CommittedMember", response.Msg.Member.GetNickname())
	require.Len(t, publisher.snapshot(), 1)
	var member model.Member
	require.NoError(t, db.Where("id = ?", fixture.MemberID).Take(&member).Error)
	require.True(t, member.Onboarded)
	require.Equal(t, "CommittedMember", member.Nickname)
	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action = ? AND target_id = ?", sharedtelemetry.AuditMemberUpdated, fixture.MemberID).
		Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestCompleteMyOnboardingAuditFailureRollsBackTransition(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createOnboardingMember(t, db, "audit-failure@example.test")
	publisher := &recordingOnboardingWelcomePublisher{}
	service := onboardingService(db, fixture, publisher)
	service.auditWriter = failingOnboardingAuditWriter{}

	_, err := service.CompleteMyOnboarding(
		withOnboardingRequestContext(t, fixture),
		connect.NewRequest(&managev1.CompleteMyOnboardingRequest{Nickname: "MustRollback"}),
	)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.ErrorContains(t, err, "audit persistence unavailable")
	require.Empty(t, publisher.snapshot())

	var member model.Member
	require.NoError(t, db.Where("id = ?", fixture.MemberID).Take(&member).Error)
	require.False(t, member.Onboarded)
	require.Equal(t, fixture.MemberID, member.Nickname)

	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action = ? AND target_id = ?", sharedtelemetry.AuditMemberUpdated, fixture.MemberID).
		Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

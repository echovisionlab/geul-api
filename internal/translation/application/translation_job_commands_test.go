package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recordingTranslationAuthorizationDomains struct {
	testTranslationDomains
	authorityCalls int
	err            error
}

type orderedSourceLocaleAuthorizationDomains struct {
	testTranslationDomains
	callOrder []string
	err       error
}

func (d *orderedSourceLocaleAuthorizationDomains) LockRoot(
	context.Context, *gorm.DB, string, string,
) error {
	d.callOrder = append(d.callOrder, "root")
	return nil
}

func (d *orderedSourceLocaleAuthorizationDomains) RequireTranslationSourceMutable(
	context.Context, *gorm.DB, string, string,
) error {
	d.callOrder = append(d.callOrder, "lifecycle")
	return nil
}

func (d *orderedSourceLocaleAuthorizationDomains) RequireEditable(
	context.Context, *gorm.DB, string, string,
) error {
	d.callOrder = append(d.callOrder, "editable")
	return nil
}

func (d *orderedSourceLocaleAuthorizationDomains) RequireSourceLocaleEdit(
	context.Context, *gorm.DB, *auth.SpiceDBClient, string, string,
) error {
	d.callOrder = append(d.callOrder, "authority")
	return d.err
}

func (d *recordingTranslationAuthorizationDomains) LockRoot(context.Context, *gorm.DB, string, string) error {
	return nil
}

func (d *recordingTranslationAuthorizationDomains) RequireEditable(context.Context, *gorm.DB, string, string) error {
	return nil
}

func (d *recordingTranslationAuthorizationDomains) RequireSourceLocaleEdit(
	_ context.Context,
	_ *gorm.DB,
	_ *auth.SpiceDBClient,
	_, _ string,
) error {
	d.authorityCalls++
	return d.err
}

func TestTranslationRegenerationUsesLockedSourceEditAuthority(t *testing.T) {
	wantErr := errors.New("source edit denied")
	domains := &recordingTranslationAuthorizationDomains{err: wantErr}
	service := &Service{domains: domains}
	err := service.requireTranslationRegenerationAuthority(
		context.Background(), &gorm.DB{}, "post", uuid.NewString(),
	)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, domains.authorityCalls)
}

func TestSourceLocaleSwitchLocksRootAndLifecycleBeforeOneExactAuthorityDecision(t *testing.T) {
	wantErr := errors.New("source edit denied")
	domains := &orderedSourceLocaleAuthorizationDomains{err: wantErr}
	service := &Service{domains: domains}
	state := &sourceLocaleSwitchState{entityType: "work", entityID: uuid.NewString()}
	require.NoError(t, service.lockSourceLocaleSwitch(context.Background(), &gorm.DB{}, state))
	err := domains.RequireSourceLocaleEdit(context.Background(), &gorm.DB{}, nil, state.entityType, state.entityID)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, []string{"root", "lifecycle", "editable", "authority"}, domains.callOrder)
}

func TestTranslationCancelUsesSourceEditAuthorityAndTerminalCAS(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	domains := &recordingTranslationAuthorizationDomains{}
	service := newTestService(t, Deps{DB: db, Domains: domains})
	service.now = func() time.Time { return time.Unix(1_700_000_300, 0).UTC() }
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	})
	running := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "cancel", uuid.NewString())

	require.NoError(t, service.CancelTranslationJob(ctx, CancelTranslationJobCommand{JobID: running.ID}))
	require.Equal(t, 1, domains.authorityCalls)
	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", running.ID).Error, gorm.ErrRecordNotFound)

	err := service.CancelTranslationJob(ctx, CancelTranslationJobCommand{JobID: running.ID})
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.Equal(t, 1, domains.authorityCalls)
}

func TestTranslationCancelRequiresAuthenticatedRequesterBeforeAuthority(t *testing.T) {
	domains := &recordingTranslationAuthorizationDomains{}
	service := &Service{domains: domains, metrics: newTranslationMetrics()}
	err := service.CancelTranslationJob(context.Background(), CancelTranslationJobCommand{JobID: uuid.NewString()})
	require.Equal(t, connect.CodeUnauthenticated, errs.CodeOf(err))
	require.Zero(t, domains.authorityCalls)
}

func TestTranslationJobProtoStatusExposesOnlyInFlightState(t *testing.T) {
	require.Equal(
		t, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_QUEUED,
		toProtoTranslationJobStatus(translationJobStatusQueued),
	)
	require.Equal(
		t, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING,
		toProtoTranslationJobStatus(translationJobStatusRunning),
	)
}

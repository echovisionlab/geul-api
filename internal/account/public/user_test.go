package public

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	accountdomain "github.com/echovisionlab/geul-api/internal/account"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestAuthorContractIncludesOptionalBio(t *testing.T) {
	t.Parallel()

	bio := (&openv1.PublicAuthorSummary{}).ProtoReflect().Descriptor().Fields().ByName("bio")
	require.NotNil(t, bio)
	require.Equal(t, protoreflect.FieldNumber(3), bio.Number())
	require.Equal(t, protoreflect.StringKind, bio.Kind())
	require.True(t, bio.HasOptionalKeyword())
}

func TestPublicAccountLifecycleHandlers(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	lifecycle := &fakePublicAccountLifecycle{scheduledAt: scheduledAt}
	service := &AccountService{lifecycle: lifecycle}
	ctx := context.Background()

	confirmed, err := service.ConfirmAccountDeletion(ctx, connect.NewRequest(&openv1.ConfirmAccountDeletionRequest{Token: "confirm"}))
	require.NoError(t, err)
	require.True(t, confirmed.Msg.Success)
	require.Equal(t, scheduledAt, confirmed.Msg.ScheduledAt.AsTime())
	require.Equal(t, "confirm", lifecycle.confirmDeletionToken)

	cancelled, err := service.CancelAccountDeletion(ctx, connect.NewRequest(&openv1.CancelAccountDeletionRequest{Token: "cancel"}))
	require.NoError(t, err)
	require.True(t, cancelled.Msg.Success)
	require.Equal(t, "cancel", lifecycle.cancelDeletionToken)

	requested, err := service.RequestAccountRecovery(ctx, connect.NewRequest(&openv1.RequestAccountRecoveryRequest{Email: "user@example.test"}))
	require.NoError(t, err)
	require.True(t, requested.Msg.Success)
	require.Equal(t, "user@example.test", lifecycle.recoveryEmail)

	recovered, err := service.ConfirmAccountRecovery(ctx, connect.NewRequest(&openv1.ConfirmAccountRecoveryRequest{Token: "recover"}))
	require.NoError(t, err)
	require.True(t, recovered.Msg.Success)
	require.Equal(t, "recover", lifecycle.confirmRecoveryToken)
}

func TestPublicAccountLifecycleHandlersPropagateErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("lifecycle failed")
	service := &AccountService{lifecycle: &fakePublicAccountLifecycle{err: wantErr}}
	ctx := context.Background()

	_, err := service.ConfirmAccountDeletion(ctx, connect.NewRequest(&openv1.ConfirmAccountDeletionRequest{}))
	require.ErrorIs(t, err, wantErr)
	_, err = service.CancelAccountDeletion(ctx, connect.NewRequest(&openv1.CancelAccountDeletionRequest{}))
	require.ErrorIs(t, err, wantErr)
	_, err = service.RequestAccountRecovery(ctx, connect.NewRequest(&openv1.RequestAccountRecoveryRequest{}))
	require.ErrorIs(t, err, wantErr)
	_, err = service.ConfirmAccountRecovery(ctx, connect.NewRequest(&openv1.ConfirmAccountRecoveryRequest{}))
	require.ErrorIs(t, err, wantErr)
}

type fakePublicAccountLifecycle struct {
	scheduledAt          time.Time
	err                  error
	confirmDeletionToken string
	cancelDeletionToken  string
	recoveryEmail        string
	confirmRecoveryToken string
}

func (f *fakePublicAccountLifecycle) ConfirmDeletion(_ context.Context, token string) (*accountdomain.AccountDeletionScheduledResult, error) {
	f.confirmDeletionToken = token
	if f.err != nil {
		return nil, f.err
	}
	return &accountdomain.AccountDeletionScheduledResult{ScheduledAt: f.scheduledAt}, nil
}

func (f *fakePublicAccountLifecycle) CancelDeletion(_ context.Context, token string) error {
	f.cancelDeletionToken = token
	return f.err
}

func (f *fakePublicAccountLifecycle) RequestRecovery(_ context.Context, email string) error {
	f.recoveryEmail = email
	return f.err
}

func (f *fakePublicAccountLifecycle) ConfirmRecovery(_ context.Context, token string) error {
	f.confirmRecoveryToken = token
	return f.err
}

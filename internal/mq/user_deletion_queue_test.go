package mq

import (
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestUserDeletionCommandIdentityIsStable(t *testing.T) {
	memberID := "14600000-0000-1000-8000-000000000001"
	for _, command := range []userDeletionCommand{
		&managev1.UserDeleteIdentityCommand{MemberId: memberID},
		&managev1.UserDeleteAvatarCommand{MemberId: memberID},
	} {
		got, err := userDeletionCommandMemberID(command)
		require.NoError(t, err)
		require.Equal(t, memberID, got)
	}
	_, err := userDeletionCommandMemberID(&managev1.UserDeleteIdentityCommand{})
	require.Error(t, err)
}

// messageIDArg matches a pgmq.send envelope whose message ID is want.
type messageIDArg string

func (want messageIDArg) Match(value driver.Value) bool {
	body, ok := value.([]byte)
	if !ok {
		return false
	}
	var envelope struct {
		MessageID string `json:"message_id"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.MessageID == string(want)
}

func TestUserDeletionCommandsEnqueueThroughTheCallersTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	memberID := "14600000-0000-1000-8000-000000000001"
	identityID := "14600000-0000-1000-8000-000000000002"
	avatarID := "14600000-0000-1000-8000-000000000003"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pgmq\.send\(`).
		WithArgs(eventpkg.QueueUserDeleteIdentity, messageIDArg("user-delete-identity:"+memberID), sqlmock.AnyArg(), 0).
		WillReturnRows(sqlmock.NewRows([]string{"send"}).AddRow(1))
	mock.ExpectQuery(`SELECT pgmq\.send\(`).
		WithArgs(eventpkg.QueueUserDeleteAvatar, messageIDArg("user-delete-avatar:"+memberID), sqlmock.AnyArg(), 0).
		WillReturnRows(sqlmock.NewRows([]string{"send"}).AddRow(2))
	mock.ExpectRollback()

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	publisher := &Publisher{} // no pool: only the caller's transaction can enqueue
	require.NoError(t, publisher.PublishUserDeleteIdentityWithExecutor(t.Context(), tx, &managev1.UserDeleteIdentityCommand{
		Mode: managev1.UserDeleteIdentityMode_TOMBSTONE, MemberId: memberID, IdentityId: identityID, AvatarAssetId: &avatarID,
	}))
	require.NoError(t, publisher.PublishUserDeleteAvatarWithExecutor(t.Context(), tx, &managev1.UserDeleteAvatarCommand{
		MemberId: memberID, AvatarAssetId: &avatarID,
	}))
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

package contentsignal_test

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"regexp"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestClientOriginInterceptor(t *testing.T) {
	origin := "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name    string
		values  []string
		want    string
		invalid bool
	}{
		{name: "legacy request"},
		{name: "tab UUID", values: []string{origin}, want: origin},
		{name: "not a UUID", values: []string{"member-1"}, invalid: true},
		{name: "padded UUID", values: []string{" " + origin}, invalid: true},
		{name: "nil UUID", values: []string{"00000000-0000-0000-0000-000000000000"}, invalid: true},
		{name: "duplicate", values: []string{origin, origin}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			next := contentsignal.ClientOriginInterceptor().WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
				called = true
				require.Equal(t, tc.want, contentsignal.ClientOrigin(ctx))
				return connect.NewResponse(&managev1.ContentUpdatedEvent{}), nil
			})
			request := connect.NewRequest(&managev1.ContentUpdatedEvent{})
			for _, value := range tc.values {
				request.Header().Add(contentsignal.ClientOriginHeader, value)
			}
			_, err := next(context.Background(), request)
			if tc.invalid {
				require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
				require.False(t, called)
			} else {
				require.NoError(t, err)
				require.True(t, called)
			}
		})
	}
}

func TestPublishInTransactionStampsClientOrigin(t *testing.T) {
	origin := "11111111-1111-4111-8111-111111111111"
	for _, source := range []managev1.ContentUpdateSource{
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_SYSTEM,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
	} {
		t.Run(source.String(), func(t *testing.T) {
			ctx, err := contentsignal.WithClientOrigin(context.Background(), origin)
			require.NoError(t, err)
			event := collabEvent()
			event.Source = source
			event.Origin = &origin // Collab cannot retain a producer-supplied origin either.
			db, mock := mockDB(t)
			expected := collabEvent()
			expected.Source = source
			if source != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB {
				expected.Origin = &origin
			}
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
				WithArgs("content.updated", sqlmock.AnyArg(), sqlmock.AnyArg(), 0).
				WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
			mock.ExpectExec(notifyQuery).
				WithArgs(eventpkg.SignalContentUpdated, matchesEnvelope{t: t, event: expected}).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			tx := db.Begin()
			require.NoError(t, contentsignal.PublishInTransaction(ctx, tx, &mq.Publisher{}, event))
			require.NoError(t, tx.Commit().Error)
			require.NoError(t, mock.ExpectationsWereMet())
			if source == managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB {
				require.Nil(t, event.Origin)
			} else {
				require.Equal(t, origin, event.GetOrigin())
			}
		})
	}
	event := collabEvent()
	event.Source = managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE
	event.Origin = &origin
	require.Error(t, contentsignal.PublishInTransaction(context.Background(), nil, &mq.Publisher{}, event))
	require.Nil(t, event.Origin)
}

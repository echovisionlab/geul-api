//go:build integration

package emaildelivery

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestEmailCourierServiceEnqueuesReservedPGMQMessageIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	publisher, err := mq.NewPublisher(pg.SQLDB)
	require.NoError(t, err)
	require.NoError(t, testutil.PurgePGMQQueue(ctx, pg.SQLDB, eventpkg.QueueEmailAuth))
	t.Cleanup(func() {
		require.NoError(t, testutil.PurgePGMQQueue(context.Background(), pg.SQLDB, eventpkg.QueueEmailAuth))
	})
	recipient := "courier-" + uuid.NewString() + "@example.test"
	issuedAt := time.Now().UTC().Add(-time.Minute)
	templateData := authCourierTemplateDataForTest(
		t,
		"registration_code_valid",
		recipient,
		"integration-courier-"+uuid.NewString(),
		issuedAt,
		map[string]interface{}{
			"registration_code":  "123456",
			"request_url":        "https://example.test/registration?flow=" + uuid.NewString(),
			"expires_in_minutes": 10,
		},
	)

	service := NewEmailCourierService(
		publisher,
		nil,
		&testAuthIssuanceAuthority{key: testEmailCourierIssuanceKey},
		15*time.Minute,
	)
	request := func() *connect.Request[intrav1.SendEmailRequest] {
		return connect.NewRequest(&intrav1.SendEmailRequest{
			Recipient:    recipient,
			TemplateType: "registration_code_valid",
			TemplateData: templateData,
		})
	}
	resp, sendErr := service.SendEmail(ctx, request())
	require.NoError(t, sendErr)
	require.True(t, resp.Msg.Queued)

	job := requirePGMQEmailSendEvent(t, pg.SQLDB, eventpkg.QueueEmailAuth)
	require.Equal(t, recipient, job.GetRecipient())
	require.NotNil(t, job.GetAuthRegistration())
	require.Equal(t, recipient, job.GetAuthRegistration().GetTargetEmail())
	require.NotEmpty(t, job.GetMessageId())
}

func requirePGMQEmailSendEvent(t *testing.T, db *sql.DB, queue string) *managev1.SendEmailEvent {
	t.Helper()

	var body []byte
	require.Eventually(t, func() bool {
		messages, err := testutil.ReadPGMQ(t.Context(), db, queue, time.Minute, 1)
		if err != nil || len(messages) == 0 {
			return false
		}
		message := messages[0]
		body, err = message.Envelope.Payload()
		require.NoError(t, err)
		require.NoError(t, testutil.CompletePGMQ(t.Context(), db, queue, message.TransportID))
		return true
	}, 5*time.Second, 50*time.Millisecond)

	var job managev1.SendEmailEvent
	require.NoError(t, proto.Unmarshal(body, &job))
	return &job
}

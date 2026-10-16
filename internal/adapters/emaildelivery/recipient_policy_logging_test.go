package emaildeliveryadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type recipientLoggingIdentityManager struct {
	auth.IdentityManager
	identity *auth.Identity
}

func (m recipientLoggingIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return m.identity, nil
}

func TestRecipientLookupFailureLogsCauseBeforeBlockedMapping(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT .* FROM "member"`).WillReturnError(errors.New("database offline for reader@example.test token=private-token"))
	identity := &auth.Identity{ID: "identity-1", ExternalID: "member-1", State: auth.KratosStateActive}
	policy := NewRecipientPolicy(db, recipientLoggingIdentityManager{identity: identity})
	messageID := "message-1"
	job := &managev1.SendEmailEvent{Recipient: "reader@example.test", TemplateType: "primary_email_changed", MessageId: &messageID, RecipientContext: email.AccountSelectedPrimaryEmailContext(identity.ID)}
	decision, err := policy.Authorize(t.Context(), job)
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, "member_identity_link_mismatch", decision.Reason)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "message-1", entry["message_id"])
	require.Equal(t, job.TemplateType, entry["template_type"])
	require.Contains(t, entry["failure_cause"], "database offline")
	require.NotContains(t, output.String(), "reader@example.test")
	require.NotContains(t, output.String(), "private-token")
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}

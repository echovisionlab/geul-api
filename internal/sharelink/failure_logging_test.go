package sharelink

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failedDeleteAuthority struct {
	recordingAuthority
	err error
}

func (a *failedDeleteAuthority) Delete(context.Context, managev1.ShareLinkEntityType, model.ShareLink, DeleteRecord) error {
	return a.err
}

// Callers decide how to answer; the error carries the redacted cause, the
// entity and the server-fault mark for the one failure record.
func TestValidationQueryFailureCarriesCauseWithoutCredentials(t *testing.T) {
	db := newManageDB(t)
	const token = "private-share-token"
	const password = "private-share-password"
	const entityID = "11111111-1111-4111-8111-111111111111"
	cause := errors.New(`query "share_link": connection reset for ` + token + " " + password)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:share_link_failure", func(tx *gorm.DB) {
		tx.AddError(cause)
	}))
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	link, err := ValidateForEntity(t.Context(), db, token, password, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, entityID)
	require.Nil(t, link)
	require.ErrorIs(t, err, cause)
	require.Empty(t, output.String())
	failure := failurecause.Of(err)
	require.True(t, failure.ServerFault)
	require.Equal(t, `load share link: query "share_link": connection reset for [redacted] [redacted]`, failure.Cause)
	require.Contains(t, failure.Attrs, slog.String("entity_id", entityID))
}

func TestDeleteFailureCarriesCauseAndEntityWithoutStoredCredentials(t *testing.T) {
	db := newManageDB(t)
	const linkID = "22222222-2222-4222-8222-222222222222"
	const entityID = "11111111-1111-4111-8111-111111111111"
	const token = "stored-private-share-token"
	const hash = "stored-private-password-hash"
	entityType := managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE
	require.NoError(t, db.Exec(`INSERT INTO share_link(id,token,entity_type,entity_id,password_hash,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`,
		linkID, token, entityType.String(), entityID, hash, time.Now().Add(time.Hour), time.Now()).Error)
	cause := errors.New(`delete from "share_link": connection reset for ` + token + " " + hash)
	service := NewService(db, &failedDeleteAuthority{err: cause})
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	response, err := service.DeleteShareLink(t.Context(), connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: linkID}))
	require.Nil(t, response)
	require.ErrorIs(t, err, cause)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	// The request record logs the failure from what the error carries.
	require.Empty(t, output.String())
	failure := failurecause.Of(err)
	require.Equal(t, `internal: delete from "share_link": connection reset for [redacted] [redacted]`, failure.Cause)
	attrs := map[string]string{}
	for _, attr := range failure.Attrs {
		attrs[attr.Key] = attr.Value.String()
	}
	require.Equal(t, map[string]string{"share_link_id": linkID, "entity_id": entityID, "entity_type": entityType.String()}, attrs)
}

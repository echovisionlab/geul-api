//go:build integration

package work_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWorkMetadataVersionRestoreAdvancesRevisionOnceIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.PrepareOryIntegrationTest(t)
	db := stack.DB
	_, ctx := newStackAdmin(t, stack)
	handler := work.NewConnectHandler(newStackService(t, db, stack.SpiceDBClient))
	current := createStackWork(t, ctx, handler, "Current work title")
	require.Len(t, current.Document.GetLocaleOverlays(), 1)
	title, summary := "Restored source title", "Restored source summary"
	encoded, err := work.EncodeVersionContentSnapshot("en", &title, &summary, &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: current.Document.GetBlockCatalogFingerprint(),
		Locale:                  "en", Base: current.Document.GetBase(), LocaleOverlay: current.Document.GetLocaleOverlays()[0],
		Profile: current.Document.GetProfile(),
	})
	require.NoError(t, err)
	versionID := uuid.NewString()
	require.NoError(t, db.Create(&model.WorkVersion{
		ID: versionID, WorkID: current.Id, Version: 1000, ContentSnapshot: encoded,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}).Error)
	restored, err := handler.RestoreWorkVersion(ctx, connect.NewRequest(&managev1.RestoreWorkVersionRequest{WorkId: current.Id, VersionId: versionID}))
	require.NoError(t, err)
	require.Equal(t, title, restored.Msg.GetTitle())
	require.Equal(t, summary, restored.Msg.GetSummary())
	require.NotEqual(t, current.Revision, restored.Msg.Revision)
	require.Equal(t, current.Status, restored.Msg.Status)
	var revision string
	require.NoError(t, db.Raw("SELECT revision FROM content_document WHERE id = (SELECT content_document_id FROM work WHERE id = ?)", current.Id).Scan(&revision).Error)
	require.Equal(t, restored.Msg.Revision, revision)
	unchanged, err := handler.RestoreWorkVersion(ctx, connect.NewRequest(&managev1.RestoreWorkVersionRequest{WorkId: current.Id, VersionId: versionID}))
	require.NoError(t, err)
	require.Equal(t, restored.Msg.Revision, unchanged.Msg.Revision, "restoring the same metadata twice must not advance the revision again")
}

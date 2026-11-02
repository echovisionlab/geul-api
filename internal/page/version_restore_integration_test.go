//go:build integration

package page_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPageMetadataVersionRestoreAdvancesRevisionOnceIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.PrepareOryIntegrationTest(t)
	db := stack.DB
	identityID := orytest.IntegrationUUID()
	memberID := orytest.SeedPostIntegrationIdentity(t, db, identityID, "Page restore admin")
	orytest.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, identityID, policyv1.Role.Admin())
	ctx := orytest.NewAuditContext(t, identityID, memberID)
	handler := page.NewConnectHandler(newStackService(t, db, stack.SpiceDBClient, stackPorts(t, db, stack.SpiceDBClient, nil)))
	created, err := handler.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Current page title"}))
	require.NoError(t, err)
	current := created.Msg
	require.Len(t, current.Document.GetLocaleOverlays(), 1)
	title, summary := "Restored source title", "Restored source summary"
	encoded, err := page.EncodeVersionContentSnapshot("en", &title, &summary, &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: current.Document.GetBlockCatalogFingerprint(),
		Locale:                  "en", Base: current.Document.GetBase(), LocaleOverlay: current.Document.GetLocaleOverlays()[0],
	})
	require.NoError(t, err)
	versionID := uuid.NewString()
	require.NoError(t, db.Create(&model.PageVersion{
		ID: versionID, PageID: current.Id, Version: 1000, ContentSnapshot: encoded,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}).Error)
	restored, err := handler.RestorePageVersion(ctx, connect.NewRequest(&managev1.RestorePageVersionRequest{PageId: current.Id, VersionId: versionID}))
	require.NoError(t, err)
	require.Equal(t, title, restored.Msg.GetTitle())
	require.Equal(t, summary, restored.Msg.GetSummary())
	require.NotEqual(t, current.Revision, restored.Msg.Revision)
	require.Equal(t, current.Status, restored.Msg.Status)
	var revision string
	require.NoError(t, db.Raw("SELECT revision FROM content_document WHERE id = (SELECT content_document_id FROM page WHERE id = ?)", current.Id).Scan(&revision).Error)
	require.Equal(t, restored.Msg.Revision, revision)
	unchanged, err := handler.RestorePageVersion(ctx, connect.NewRequest(&managev1.RestorePageVersionRequest{PageId: current.Id, VersionId: versionID}))
	require.NoError(t, err)
	require.Equal(t, restored.Msg.Revision, unchanged.Msg.Revision, "restoring the same metadata twice must not advance the revision again")
}

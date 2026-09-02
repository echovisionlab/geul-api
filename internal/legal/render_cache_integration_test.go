//go:build integration

package legal_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLegalDerivedContentRegenerationUsesExactTypedDocumentIntegration(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			db := newLegalIntegrationDB(t)
			ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
			store, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
			require.NoError(t, err)
			sourceText := kind + " semantic policy body"

			var entityID string
			var revision string
			var canonicalHash string
			var managementSnapshotDigest string
			var regenerate func(context.Context, string) error
			if kind == "terms" {
				service := newTermsServiceForLegalIntegrationTest(
					db, "", "", spiceDB, legaldomain.WithTermsContentBlockStore(store),
				)
				created, createErr := service.CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{
					Title:    ptrString("Legal render terms"),
					Document: legalPolicyDocumentFixture("en", sourceText),
				}))
				require.NoError(t, createErr)
				entityID = created.Msg.Id
				revision = created.Msg.Revision
				managementSnapshotDigest = created.Msg.SnapshotDigest
				regenerate = func(ctx context.Context, expectedHash string) error {
					_, regenerateErr := service.RegenerateTermsDerivedContent(
						ctx,
						connect.NewRequest(&managev1.RegenerateTermsDerivedContentRequest{
							Id: entityID, ExpectedSnapshotDigest: expectedHash,
						}),
					)
					return regenerateErr
				}
			} else {
				service := newPrivacyServiceForLegalIntegrationTest(
					db, "", "", spiceDB, legaldomain.WithPrivacyContentBlockStore(store),
				)
				created, createErr := service.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
					Title:    ptrString("Legal render privacy"),
					Document: legalPolicyDocumentFixture("en", sourceText),
				}))
				require.NoError(t, createErr)
				entityID = created.Msg.Id
				revision = created.Msg.Revision
				managementSnapshotDigest = created.Msg.SnapshotDigest
				regenerate = func(ctx context.Context, expectedHash string) error {
					_, regenerateErr := service.RegeneratePrivacyDerivedContent(
						ctx,
						connect.NewRequest(&managev1.RegeneratePrivacyDerivedContentRequest{
							Id: entityID, ExpectedSnapshotDigest: expectedHash,
						}),
					)
					return regenerateErr
				}
			}

			require.NoError(t, db.Table(kind+"_history").Select("content_hash").Where("id = ?", entityID).Scan(&canonicalHash).Error)
			require.NotEmpty(t, canonicalHash)
			require.Equal(t, canonicalHash, managementSnapshotDigest)
			for _, status := range legalRenderLifecycleStatuses(kind) {
				require.NoError(t, db.Table(kind+"_history").Where("id = ?", entityID).
					Update("status", status).Error)
				require.NoError(t, db.Table(kind+"_history").Where("id = ?", entityID).
					Updates(map[string]any{
						"content": "<p>corrupt caller HTML</p>", "content_text": "corrupt caller text",
						"content_hash": "corrupt", "view_hash": "corrupt",
					}).Error)

				require.Equal(
					t,
					connect.CodeFailedPrecondition,
					connect.CodeOf(regenerate(ctx, "stale-canonical-hash")),
				)
				require.NoError(t, regenerate(ctx, canonicalHash))
				requireLegalSemanticProjection(
					t, db, kind+"_history", entityID, canonicalHash, sourceText,
				)
			}

			var state struct {
				Revision     string `gorm:"column:revision"`
				SourceLocale string `gorm:"column:source_locale"`
			}
			require.NoError(t, db.Raw(`
				SELECT cd.revision::text AS revision, h.source_locale
				FROM `+kind+`_history h
				JOIN content_document cd ON cd.id = h.content_document_id
				WHERE h.id = ?
			`, entityID).Scan(&state).Error)
			require.Equal(t, revision, state.Revision)
			require.Equal(t, "en", state.SourceLocale)
		})
	}
}

func legalRenderLifecycleStatuses(kind string) []string {
	if kind == "privacy" {
		return []string{
			managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(),
			managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
		}
	}
	return []string{
		managev1.TermsStatus_TERMS_STATUS_DRAFT.String(),
		managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
		managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
		managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
	}
}

func requireLegalSemanticProjection(
	t *testing.T,
	db *gorm.DB,
	table string,
	entityID string,
	wantCanonicalHash string,
	wantText string,
) {
	t.Helper()
	var row struct {
		Content     string `gorm:"column:content"`
		ContentText string `gorm:"column:content_text"`
		ContentHash string `gorm:"column:content_hash"`
		ViewHash    string `gorm:"column:view_hash"`
	}
	require.NoError(t, db.Raw(
		"SELECT content, content_text, content_hash, view_hash FROM "+table+" WHERE id = ?",
		entityID,
	).Scan(&row).Error)
	require.NotContains(t, row.Content, "corrupt caller HTML")
	require.Contains(t, row.Content, wantText)
	require.Contains(t, row.ContentText, wantText)
	require.Equal(t, wantCanonicalHash, row.ContentHash)
	require.Equal(t, wantCanonicalHash, row.ViewHash)
}

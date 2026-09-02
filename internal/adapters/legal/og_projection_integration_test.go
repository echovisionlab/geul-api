//go:build integration

package legal

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProjectionBindsCanonicalLocalizedLegalRoutesIntegration(t *testing.T) {
	for _, testCase := range []struct {
		kind   string
		status string
	}{
		{kind: "privacy", status: "PRIVACY_STATUS_ACTIVE"},
		{kind: "terms", status: "TERMS_STATUS_ACTIVE"},
	} {
		t.Run(testCase.kind, func(t *testing.T) {
			pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
			db := pg.DB
			now := time.Now().UTC()
			documentID, assetID := uuid.NewString(), uuid.NewString()
			documentRevision := uuid.NewString()
			require.NoError(t, db.Exec(`
				INSERT INTO content_document (id, profile, revision, created_at, updated_at)
				VALUES (?::uuid, 'policy', ?::uuid, ?, ?)`,
				documentID, documentRevision, now, now,
			).Error)
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.kind+"_history (id, version, status, effective_from, title, content, content_document_id, source_locale) VALUES (?::uuid, 1, ?, ?, 'Legal title', '', ?::uuid, 'en')",
				documentID, testCase.status, now.Add(-time.Minute), documentID,
			).Error)
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.kind+"_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, 'ko', 'Localized title', ?, ?)",
				documentID, now, now,
			).Error)
			fileSize := int64(123)
			digest := make([]byte, 32)
			objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
			require.NoError(t, err)
			require.NoError(t, db.Create(&model.PublicAsset{
				ID: assetID, Kind: "og", ObjectKey: objectKey,
				Extension: "webp", MimeType: "image/webp", FileSize: &fileSize,
				SHA256: digest, Disposition: "inline", Status: model.PublicAssetStatusReady,
				ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
			}).Error)
			locale := "ko"
			require.NoError(t, NewProjection().Complete(t.Context(), db, og.Target{
				EntityType: testCase.kind, EntityID: RouteID(testCase.kind), Locale: &locale, Kind: "locale",
			}, assetID, now, "https://cdn.example.com"))
			var binding model.PublicAssetBinding
			require.NoError(t, db.First(&binding,
				"owner_type = ? AND owner_id = ? AND binding_key = ?",
				testCase.kind, RouteID(testCase.kind), "og:ko",
			).Error)
			require.Equal(t, assetID, binding.AssetID)
		})
	}
}

func TestProjectionRejectsNonCanonicalLegalRoute(t *testing.T) {
	locale := "en"
	err := NewProjection().ReleasePending(t.Context(), &gorm.DB{}, og.Target{
		EntityType: "privacy", EntityID: uuid.NewString(), Locale: &locale, Kind: "locale",
	}, "")
	require.ErrorContains(t, err, "canonical route identity")
}

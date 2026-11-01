//go:build integration

package series_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/series"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// integrationDeps are the parts of a Series service an integration test
// chooses; the rest are the production adapters over db.
type integrationDeps struct {
	spiceDB    series.Authorizer
	client     *auth.SpiceDBClient
	fence      identitystate.Fence
	postAccess seriespostgres.PostAccess
}

// newIntegrationService serves the Series use cases over the PostgreSQL
// store and the production Post, media, OG and Menu adapters.
func newIntegrationService(t *testing.T, db *gorm.DB, deps integrationDeps) *series.Service {
	t.Helper()
	runtime := seriesadapter.NewRuntime(db, "", newOGRefresher(db))
	if deps.client == nil {
		deps.client = &auth.SpiceDBClient{}
	}
	if deps.postAccess == nil {
		deps.postAccess = seriesadapter.PostAccess{}
	}
	if deps.fence == nil {
		deps.fence = identitystate.PostgresFence{}
	}
	units, err := seriespostgres.NewUnits(db, seriespostgres.Collaborators{
		Media: runtime, MenuTargets: menuadapter.NewTargetLifecycle(&audittest.Recorder{}), Posts: postadapter.SeriesMembership{},
		PostAccess: deps.postAccess, SpiceDB: deps.client, Checkpoints: unreachableCheckpoints{},
	})
	require.NoError(t, err)
	locales, err := seriespostgres.NewLocales(db)
	require.NoError(t, err)
	service, err := series.NewService(series.Deps{
		Units: units, SpiceDB: deps.spiceDB, Fence: deps.fence, Audit: &audittest.Recorder{},
		Media: runtime, Reads: runtime, Members: seriesadapter.NewMemberSummaries(db, ""), Locales: locales,
	})
	require.NoError(t, err)
	return service
}

type unreachableCheckpoints struct{}

func (unreachableCheckpoints) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errors.New("collaboration checkpoints are not part of this test")
}

// allowedPostEdits stands in for the Post edit decision, which needs SpiceDB
// and Kratos rows.
type allowedPostEdits struct{}

func (allowedPostEdits) RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	return nil
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newOGRefresher(db *gorm.DB) *ogpostgres.Refresher {
	planner := ogpostgres.NewPlanner("https://cdn.example.com", renderConfig{}, seriesadapter.NewProjection())
	return ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(seriesadapter.NewRequests()))
}

// seedSeriesRow stores a Series, its compact content document and its
// English source copy.
func seedSeriesRow(t *testing.T, db *gorm.DB, title, slug string, status model.SeriesStatus) string {
	t.Helper()
	id := testutil.IntegrationUUID()
	now := time.Now().UTC()
	contentDocumentID := testutil.IntegrationUUID()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO content_document (id, profile, revision, created_at, updated_at)
			VALUES (?::uuid, 'compact', ?::uuid, ?, ?)`, contentDocumentID, testutil.IntegrationUUID(), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO series (id, slug, status, source_locale, content_document_id, created_at, updated_at)
			VALUES (?::uuid, ?, ?, 'en', ?::uuid, ?, ?)`, id, slug, status, contentDocumentID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO series_translation (entity_id, locale, title, created_at, updated_at)
			VALUES (?::uuid, 'en', ?, ?, ?)`, id, title, now, now).Error
	}))
	return id
}

// seedImageFile stores a File with a ready image asset.
func seedImageFile(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	fileID := testutil.IntegrationUUID()
	assetID := testutil.IntegrationUUID()
	digest := sha256.Sum256([]byte(key))
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "series-image-" + fileID, MimeType: "image/webp",
		FileSize: fileSize, Extension: "webp", SHA256: digest[:],
	}).Error)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "image", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize, SHA256: digest[:],
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID
}

func requireCount(t *testing.T, db *gorm.DB, want int64, table, where string, args ...any) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).Where(where, args...).Count(&count).Error)
	require.Equal(t, want, count, "%s where %s", table, where)
}

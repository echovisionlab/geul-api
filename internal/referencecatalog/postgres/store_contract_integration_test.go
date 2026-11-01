//go:build integration

package referencecatalogpostgres_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
	"github.com/echovisionlab/geul-api/internal/referencecatalog/referencecatalogtest"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheReferenceCatalogStoreContractIntegration(t *testing.T) {
	t.Parallel()
	referencecatalogtest.RunStoreContract(t, func(t *testing.T) referencecatalogtest.Harness {
		db := newDatabase(t)
		fixtures := fixtures{db: db}
		return referencecatalogtest.Harness{
			Harness:          mutationtest.PostgresHarness(db, newUnits(t, db, &audittest.Recorder{})),
			SeedReferenceUse: fixtures.referenceUse,
			SeedClientWork:   fixtures.clientWork,
			SeedMapPlaceUse:  fixtures.mapPlaceUse,
			SeedMember: func(t *testing.T, nickname string) string {
				return orytest.SeedPostIntegrationIdentity(t, db, uuid.NewString(), nickname)
			},
			SeedReadyFile: fixtures.readyFile,
		}
	})
}

// newDatabase is a database of its own with the app schema; the Members
// the Store resolves are linked to Kratos stub identities.
func newDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
}

// newUnits is the production unit of work with the production adapters; the
// Menu target lifecycle appends its audit through menuAudit.
func newUnits(t *testing.T, db *gorm.DB, menuAudit domainaudit.Appender) mutation.UnitOfWork[referencecatalog.Store] {
	t.Helper()
	units, err := referencecatalogpostgres.NewUnits(db, referencecatalogpostgres.Ports{
		Assets:      referencecatalogadapter.NewAssets("https://cdn.example.com"),
		Members:     referencecatalogadapter.NewMemberSummaries("https://cdn.example.com"),
		MenuTargets: referencecatalogmenuadapter.NewTargets(menuAudit),
		ClientWorks: workadapter.ClientWorks{},
	})
	require.NoError(t, err)
	return units
}

// fixtures seeds the rows other domains own with SQL.
type fixtures struct{ db *gorm.DB }

func (f fixtures) document(t *testing.T, profile string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)`, id, profile).Error)
	return id
}

func (f fixtures) work(t *testing.T, mapPlaceID *string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, f.db.Exec(`
		INSERT INTO work (id, type, year, month, until_year, until_month, map_place_id, content_document_id)
		VALUES (?::uuid, 'WORK_TYPE_PORTFOLIO', 2026, 1, 2026, 1, ?::uuid, ?::uuid)`,
		id, mapPlaceID, f.document(t, "work")).Error)
	return id
}

// referenceUse links the reference to a new Post (category, tag) or Release
// (genre, style, format).
func (f fixtures) referenceUse(t *testing.T, kind referencecatalog.ReferenceKind, id string) {
	t.Helper()
	switch kind {
	case referencecatalog.KindCategory, referencecatalog.KindTag:
		postID := uuid.NewString()
		require.NoError(t, f.db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?::uuid, ?::uuid)`, postID, f.document(t, "post")).Error)
		require.NoError(t, f.db.Exec(`INSERT INTO post_`+string(kind)+` (post_id, `+string(kind)+`_id) VALUES (?::uuid, ?::uuid)`, postID, id).Error)
	default:
		releaseID := uuid.NewString()
		require.NoError(t, f.db.Exec(`INSERT INTO release (id, type, content_document_id) VALUES (?::uuid, 'RELEASE_TYPE_ALBUM', ?::uuid)`,
			releaseID, f.document(t, "compact")).Error)
		require.NoError(t, f.db.Exec(`INSERT INTO release_`+string(kind)+` (release_id, `+string(kind)+`_id) VALUES (?::uuid, ?::uuid)`, releaseID, id).Error)
	}
}

func (f fixtures) clientWork(t *testing.T, clientID string) {
	t.Helper()
	require.NoError(t, f.db.Exec(`INSERT INTO work_client (work_id, client_id) VALUES (?::uuid, ?::uuid)`, f.work(t, nil), clientID).Error)
}

func (f fixtures) mapPlaceUse(t *testing.T, placeID string) {
	t.Helper()
	f.work(t, &placeID)
}

// readyFile stores a file and its ready public asset of kind.
func (f fixtures) readyFile(t *testing.T, kind string) (string, string) {
	t.Helper()
	fileID, assetID := uuid.NewString(), uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	now := time.Now().UTC()
	require.NoError(t, f.db.Create(&model.File{
		ID: fileID, FileName: "catalog-" + fileID, MimeType: "image/webp",
		FileSize: 1024, Extension: "webp", SHA256: digest[:], CreatedAt: now,
	}).Error)
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	fileSize := int64(1024)
	require.NoError(t, f.db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: kind, ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize,
		SHA256: digest[:], Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID, assetID
}

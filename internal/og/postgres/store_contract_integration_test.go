//go:build integration

package ogpostgres_test

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/og/ogtest"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	ogtest.RunStoreContract(t, func(t *testing.T) ogtest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		references := &referenceSet{assets: map[string]bool{}}
		units, err := ogpostgres.NewUnits(db, ogpostgres.Ports{
			CDNDomain: ogtest.CDNDomain, RenderConfig: renderConfig{}, Projections: []ogpostgres.Projection{projection{}},
			Requests: ogpostgres.NewResolver(source{}), AllRequests: ogpostgres.NewCollector(source{}),
			Reconciler: ogpostgres.NoopGlobalReconciler{}, References: []ogpostgres.AssetReference{references},
		})
		require.NoError(t, err)
		return ogtest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedFile: func(t *testing.T) string {
				id := uuid.NewString()
				digest := sha256.Sum256([]byte(id))
				require.NoError(t, db.Create(&model.File{
					ID: id, FileName: "source", MimeType: "image/webp", FileSize: 1024,
					Extension: "webp", SHA256: digest[:], CreatedAt: time.Now().UTC(),
				}).Error)
				return id
			},
			SeedReadyAsset: func(t *testing.T, kind, sourceFileID string, at time.Time) og.Asset {
				asset := ogtest.ReadyAsset(kind, at)
				if sourceFileID != "" {
					asset.SourceFileID = &sourceFileID
				}
				require.NoError(t, db.Create(&model.PublicAsset{
					ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: kind, ObjectKey: "asset/" + asset.ID + ".webp",
					Extension: asset.Extension, MimeType: asset.MimeType, FileSize: asset.FileSize, SHA256: asset.SHA256,
					Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: asset.ReadyAt,
					CreatedAt: at, UpdatedAt: at,
				}).Error)
				return asset
			},
			SeedBinding: func(t *testing.T, assetID string) {
				now := time.Now().UTC()
				require.NoError(t, db.Create(&model.PublicAssetBinding{
					AssetID: assetID, OwnerType: "work", OwnerID: uuid.NewString(), BindingKey: "og",
					CreatedAt: now, UpdatedAt: now,
				}).Error)
			},
			SeedReference: func(_ *testing.T, assetID string) { references.add(assetID) },
		}
	})
}

// The stand-ins below are the ports of other domains; the contract checks
// OG's own SQL.

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":""}`)
	return payload, ogtest.Revision(payload), nil
}

type projection struct{}

func (projection) Handles(og.Target) bool { return true }
func (projection) ReleasePending(context.Context, *gorm.DB, og.Target, string) error {
	return nil
}
func (projection) Complete(context.Context, *gorm.DB, og.Target, string, time.Time, string) error {
	return nil
}

type source struct{}

func (source) Handles(string) bool { return true }
func (source) Resolve(context.Context, *gorm.DB, string, string, *managev1.OgTargetSelection) ([]og.Request, error) {
	return nil, nil
}
func (source) All(context.Context, *gorm.DB) ([]og.Request, error) { return nil, nil }

type referenceSet struct {
	mu     sync.Mutex
	assets map[string]bool
}

func (r *referenceSet) add(assetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.assets[assetID] = true
}

func (r *referenceSet) ReferencesAsset(_ context.Context, _ *gorm.DB, assetID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.assets[assetID], nil
}

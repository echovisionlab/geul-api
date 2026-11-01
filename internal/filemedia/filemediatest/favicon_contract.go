package filemediatest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func readyFaviconAsset(spec favicon.Spec) mediaasset.Asset {
	id := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return mediaasset.Asset{ID: id, Kind: "favicon", ObjectKey: "asset/" + id + "." + spec.Extension, Extension: spec.Extension, MimeType: spec.MimeType, FileSize: new(int64(64)), SHA256: bytes.Repeat([]byte{5}, 32), Disposition: "inline", Status: mediaasset.AssetReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now}
}

func faviconCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"favicon source projection includes pending Files and maps missing sources": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("favicon", nil)
			h.SeedFile(t, row, true, false)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				for _, lock := range []bool{false, true} {
					got, err := s.FaviconSource(ctx, row.ID, lock)
					require.NoError(t, err)
					require.Equal(t, row.ID, got.ID)
					require.Equal(t, *row.MimeType, got.MimeType)
					require.Equal(t, *row.Extension, got.Extension)
					require.Equal(t, *row.FileSize, got.FileSize)
					require.Nil(t, got.DeleteRequestedAt)
				}
				_, err := s.FaviconSource(ctx, uuid.NewString(), false)
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				return nil
			}))
		},
		"favicon bundle inserts all derivative identities before projection": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("favicon", nil)
			row.Extension = new("png")
			row.MimeType = new("image/png")
			h.SeedFile(t, row, false, false)
			assets := map[favicon.DerivativeType]mediaasset.Asset{}
			for _, spec := range favicon.RequiredOutputs() {
				a := readyFaviconAsset(spec)
				h.SeedAsset(t, a)
				assets[spec.DerivativeType] = a
			}
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				set, err := s.FaviconSet(ctx, "https://cdn.example.com", row.ID)
				require.NoError(t, err)
				require.Nil(t, set)
				_, err = s.FaviconSource(ctx, row.ID, true)
				require.NoError(t, err)
				for typ, a := range assets {
					require.NoError(t, s.InsertFaviconDerivative(ctx, uuid.NewString(), row.ID, typ.String(), a.ID))
				}
				set, err = s.FaviconSet(ctx, "https://cdn.example.com", row.ID)
				require.NoError(t, err)
				require.NotNil(t, set)
				require.Equal(t, assets[favicon.PNG32].ID, set.IconPNG32.AssetID)
				require.Len(t, favicon.AssetIDs(set), 7)
				require.Nil(t, set.IconSVG)
				return nil
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				set, err := s.FaviconSet(ctx, "https://cdn.example.com", row.ID)
				require.NoError(t, err)
				require.Len(t, favicon.AssetIDs(set), 7)
				return nil
			}))
		},
		"duplicate favicon derivative rolls back the whole owner transaction": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("favicon", nil)
			h.SeedFile(t, row, false, false)
			a := readyFaviconAsset(favicon.RequiredOutputs()[0])
			h.SeedAsset(t, a)
			require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.InsertFaviconDerivative(ctx, uuid.NewString(), row.ID, favicon.ICO.String(), a.ID))
				return s.InsertFaviconDerivative(ctx, uuid.NewString(), row.ID, favicon.ICO.String(), a.ID)
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				rows, err := s.StoredDerivativeDeliveries(ctx, []string{row.ID})
				require.NoError(t, err)
				require.Empty(t, rows)
				return nil
			}))
		},
		"favicon source update lock blocks another finalizer": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("favicon", nil)
			h.SeedFile(t, row, false, false)
			lock := func(ctx context.Context, s domain.Store) error {
				_, err := s.FaviconSource(ctx, row.ID, true)
				return err
			}
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, lock, func(context.Context, domain.Store) error { return nil }, lock))
		},
	}
}

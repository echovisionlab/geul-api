package favicon_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/favicon/favicontest"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
)

func readyAsset(kind, extension, mimeType string, createdAt time.Time) favicon.DerivativeAsset {
	id := uuid.NewString()
	key, _ := mediaauth.AssetObjectKey(id, extension)
	digest := sha256.Sum256([]byte(id))
	return favicon.DerivativeAsset{
		ID: id, Kind: kind, ObjectKey: key, Extension: extension, MimeType: mimeType, FileSize: new(int64(64)),
		SHA256: digest[:], Disposition: "inline", Status: favicon.AssetReady, CreatedAt: createdAt,
	}
}

func seedGeneratedSet(store *favicontest.Store, fileID string) map[string]favicon.DerivativeAsset {
	set := map[string]favicon.DerivativeAsset{}
	for _, spec := range favicon.RequiredOutputs() {
		asset := readyAsset("favicon", spec.Extension, spec.MimeType, time.Now())
		store.SeedAsset(fileID, spec.DerivativeType.String(), asset)
		set[spec.DerivativeType.String()] = asset
	}
	return set
}

func TestLoadSetReturnsACompleteVerifiedSetOrNothing(t *testing.T) {
	store, _ := favicontest.New()
	generated, legacy, broken := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store.SeedFile(generated, "image/png")
	store.SeedFile(legacy, "image/png")
	store.SeedFile(broken, "image/png")
	assets := seedGeneratedSet(store, generated)
	partial := readyAsset("favicon", "ico", "image/vnd.microsoft.icon", time.Now())
	store.SeedAsset(broken, "FILE_DERIVATIVE_TYPE_FAVICON_ICO", partial)

	set, err := favicon.LoadSet(t.Context(), store, " "+generated+" ")
	require.NoError(t, err)
	require.Equal(t, assets["FILE_DERIVATIVE_TYPE_FAVICON_PNG_32"].ID, set.IconPNG32.AssetID)
	require.Equal(t, assets["FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_512"].ID, set.Manifest512.AssetID)
	require.Nil(t, set.IconSVG)

	for _, fileID := range []string{legacy, uuid.NewString(), " "} {
		set, err = favicon.LoadSet(t.Context(), store, fileID)
		require.NoError(t, err)
		require.Nil(t, set)
	}
	_, err = favicon.LoadSet(t.Context(), store, broken)
	require.ErrorContains(t, err, "has 1 rows, want 7")
}

func TestLoadSetRejectsAGeneratedAssetWithInvalidMetadata(t *testing.T) {
	for name, corrupt := range map[string]func(*favicon.DerivativeAsset){
		"not ready":       func(asset *favicon.DerivativeAsset) { asset.Status = favicon.AssetAllocated },
		"wrong kind":      func(asset *favicon.DerivativeAsset) { asset.Kind = "image" },
		"made from file":  func(asset *favicon.DerivativeAsset) { asset.SourceFileID = new(uuid.NewString()) },
		"short digest":    func(asset *favicon.DerivativeAsset) { asset.SHA256 = []byte{1} },
		"foreign key":     func(asset *favicon.DerivativeAsset) { asset.ObjectKey = "asset/other.png" },
		"missing asset":   func(asset *favicon.DerivativeAsset) { asset.ID = "" },
		"wrong mime type": func(asset *favicon.DerivativeAsset) { asset.MimeType = "image/jpeg" },
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := favicontest.New()
			fileID := uuid.NewString()
			store.SeedFile(fileID, "image/png")
			assets := seedGeneratedSet(store, fileID)
			asset := assets["FILE_DERIVATIVE_TYPE_FAVICON_PNG_16"]
			corrupt(&asset)
			store.SeedAsset(fileID, "FILE_DERIVATIVE_TYPE_FAVICON_PNG_16", asset)

			_, err := favicon.LoadSet(t.Context(), store, fileID)
			require.Error(t, err)
		})
	}
}

func TestLoadSetOfAnSVGSourceAddsItsReadySourceAsset(t *testing.T) {
	store, _ := favicontest.New()
	fileID := uuid.NewString()
	store.SeedFile(fileID, "image/svg+xml")
	seedGeneratedSet(store, fileID)

	_, err := favicon.LoadSet(t.Context(), store, fileID)
	require.ErrorContains(t, err, "SVG favicon source asset is not ready")
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))

	svg := readyAsset("favicon", "svg", "image/svg+xml", time.Now())
	svg.SourceFileID = &fileID
	store.SeedAsset(fileID, "", svg)
	set, err := favicon.LoadSet(t.Context(), store, fileID)
	require.NoError(t, err)
	require.Equal(t, svg.ID, set.IconSVG.AssetID)
	require.Len(t, favicon.AssetIDs(set), 8)

	legacy, generated := favicon.Projection(t.Context(), store, fileID)
	require.Equal(t, set.IconPNG32.AssetID, legacy.AssetID)
	require.NotNil(t, generated)
}

func TestProjectionFallsBackToTheReadySourceAsset(t *testing.T) {
	store, _ := favicontest.New()
	fileID := uuid.NewString()
	store.SeedFile(fileID, "image/png")
	older, newer := readyAsset("favicon", "png", "image/png", time.Now().Add(-time.Hour)), readyAsset("favicon", "png", "image/png", time.Now())
	older.SourceFileID, newer.SourceFileID = &fileID, &fileID
	store.SeedAsset(fileID, "", older)
	store.SeedAsset(fileID, "", newer)

	legacy, set := favicon.Projection(t.Context(), store, fileID)
	require.Nil(t, set)
	require.Equal(t, newer.ID, legacy.AssetID)
	legacy, set = favicon.Projection(t.Context(), store, uuid.NewString())
	require.Nil(t, legacy)
	require.Nil(t, set)
}

func TestRequestDeletionRetiresOnlyUnboundFaviconAssets(t *testing.T) {
	t.Run("a generated set and its source asset", func(t *testing.T) {
		store, _ := favicontest.New()
		fileID := uuid.NewString()
		assets := seedGeneratedSet(store, fileID)
		source := readyAsset("favicon", "png", "image/png", time.Now())
		source.SourceFileID = &fileID
		store.SeedAsset(fileID, "", source)
		deleted := assets["FILE_DERIVATIVE_TYPE_FAVICON_ICO"]
		deleted.Status = favicon.AssetDeleted
		store.SeedAsset(fileID, "FILE_DERIVATIVE_TYPE_FAVICON_ICO", deleted)

		require.NoError(t, favicon.RequestDeletion(t.Context(), store, fileID))
		require.Len(t, store.Deletions(), 7)
		require.Equal(t, favicon.AssetDeletePending, store.Status(source.ID))
		require.Equal(t, favicon.AssetDeleted, store.Status(deleted.ID))
	})
	t.Run("a bound asset blocks the deletion", func(t *testing.T) {
		store, _ := favicontest.New()
		fileID := uuid.NewString()
		assets := seedGeneratedSet(store, fileID)
		store.SeedBinding(assets["FILE_DERIVATIVE_TYPE_FAVICON_PNG_32"].ID)

		err := favicon.RequestDeletion(t.Context(), store, fileID)
		require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
		require.Empty(t, store.Deletions())
	})
	t.Run("a non-favicon derivative or an allocated asset blocks it", func(t *testing.T) {
		for name, corrupt := range map[string]func(*favicon.DerivativeAsset){
			"non-favicon": func(asset *favicon.DerivativeAsset) { asset.Kind = "image" },
			"allocated":   func(asset *favicon.DerivativeAsset) { asset.Status = favicon.AssetAllocated },
		} {
			t.Run(name, func(t *testing.T) {
				store, _ := favicontest.New()
				fileID := uuid.NewString()
				asset := readyAsset("favicon", "ico", "image/vnd.microsoft.icon", time.Now())
				corrupt(&asset)
				store.SeedAsset(fileID, "FILE_DERIVATIVE_TYPE_FAVICON_ICO", asset)
				require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(favicon.RequestDeletion(t.Context(), store, fileID)))
			})
		}
	})
	t.Run("a File without favicon assets has nothing to retire", func(t *testing.T) {
		store, _ := favicontest.New()
		require.NoError(t, favicon.RequestDeletion(t.Context(), store, uuid.NewString()))
		require.NoError(t, favicon.RequestDeletion(t.Context(), store, " "))
		require.Empty(t, store.Deletions())
	})
}

func TestCleanupMarksOnlyOldUnboundReadyFaviconAssets(t *testing.T) {
	store, units := favicontest.New()
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	dangling, active := readyAsset("favicon", "png", "image/png", old), readyAsset("favicon", "png", "image/png", old)
	young, image := readyAsset("favicon", "png", "image/png", now.Add(-23*time.Hour)), readyAsset("image", "png", "image/png", old)
	for _, asset := range []favicon.DerivativeAsset{dangling, active, young, image} {
		store.SeedAsset("", "", asset)
	}
	store.SeedBinding(active.ID)

	cleanup := favicon.NewCleanup(units)
	require.NoError(t, cleanup.MarkDanglingReadyAssets(t.Context(), now.Add(-favicon.DanglingAssetRetention), now))
	require.Equal(t, favicon.AssetDeletePending, store.Status(dangling.ID))
	for _, kept := range []favicon.DerivativeAsset{active, young, image} {
		require.Equal(t, favicon.AssetReady, store.Status(kept.ID))
	}

	locked := false
	changed, err := cleanup.MarkDanglingReadyAsset(t.Context(), young.ID, now, now, func() { locked = true })
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, locked)
	changed, err = cleanup.MarkDanglingReadyAsset(t.Context(), uuid.NewString(), now, now, nil)
	require.NoError(t, err)
	require.False(t, changed)
}

package mediaasset

import (
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func seedAllocatedMediaGeneration(
	t *testing.T,
	service *Lifecycle,
	fileID string,
) (*model.MediaGeneration, *commonv1.MediaGenerationWriteTarget) {
	t.Helper()
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	now := service.now().UTC()
	generation := &model.MediaGeneration{
		ID:           generationID,
		FileID:       fileID,
		Kind:         "hls",
		ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8",
		Status:       model.MediaGenerationStatusAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	require.NoError(t, service.db.Create(generation).Error)
	return generation, &commonv1.MediaGenerationWriteTarget{
		GenerationId: generation.ID,
		FileId:       generation.FileID,
		ObjectPrefix: generation.ObjectPrefix,
	}
}

func TestMediaAssetLifecyclePublicAsset(t *testing.T) {
	db := newUnitDB(t)
	now := time.Date(2026, time.July, 10, 8, 0, 0, 0, time.UTC)
	service := NewLifecycle(db, "https://cdn.example.com/")
	service.now = func() time.Time { return now }
	sourceFileID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO file (id, delete_requested_at) VALUES (?, NULL)", sourceFileID,
	).Error)

	_, _, err := service.AllocatePublicAsset(t.Context(), Allocation{
		Kind:        "unknown",
		Extension:   "webp",
		MimeType:    "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.Error(t, err)

	asset, target, err := service.AllocatePublicAsset(t.Context(), Allocation{
		SourceFileID: &sourceFileID,
		Kind:         "image",
		Extension:    "webp",
		MimeType:     "image/webp; charset=binary",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	require.Equal(t, "asset/"+asset.ID+".webp", target.GetObjectKey())
	require.Equal(t, model.PublicAssetStatusAllocated, asset.Status)

	digest := sha256.Sum256([]byte("asset bytes"))
	_, err = service.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId:  asset.ID,
		FileSize: 0,
		Sha256:   digest[:],
	})
	require.Error(t, err)

	result := &commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 123, Sha256: digest[:]}
	ready, err := service.CompletePublicAsset(t.Context(), result)
	require.NoError(t, err)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
	require.Equal(t, int64(123), *ready.FileSize)

	ready, err = service.CompletePublicAsset(t.Context(), result)
	require.NoError(t, err)
	ref, err := service.ReadyAssetRef(t.Context(), ready.ID)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/asset/"+ready.ID+"/image.webp", ref.GetUrl())
	require.Equal(t, digest[:], ref.GetSha256())

	conflictingDigest := sha256.Sum256([]byte("other bytes"))
	_, err = service.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId:  asset.ID,
		FileSize: 123,
		Sha256:   conflictingDigest[:],
	})
	require.Error(t, err)

	ownerID := uuid.NewString()
	bound, err := service.BindReadyAssetForSourceFile(
		t.Context(), sourceFileID, "post", ownerID, "featured_image", "image",
	)
	require.NoError(t, err)
	require.Equal(t, asset.ID, bound.GetAssetId())
	err = service.RequestPublicAssetDeletion(t.Context(), asset.ID)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	require.NoError(t, service.ReleasePublicAssetBindings(t.Context(), "post", ownerID, "featured_image"))
	var deleting model.PublicAsset
	require.NoError(t, db.First(&deleting, "id = ?", asset.ID).Error)
	require.Equal(t, model.PublicAssetStatusReady, deleting.Status)
	require.Nil(t, deleting.DeleteRequestedAt)
	require.NoError(t, service.RequestPublicAssetDeletion(t.Context(), asset.ID))
	require.NoError(t, db.First(&deleting, "id = ?", asset.ID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, deleting.Status)
	require.Equal(t, now, *deleting.DeleteRequestedAt)
}

func TestMediaAssetLifecycleLastBindingDeletion(t *testing.T) {
	t.Run("one of many bindings keeps asset ready", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		asset := allocateAndCompleteTestPublicAsset(t, service, validGeneratedPublicAssetAllocation())
		firstOwnerID := uuid.NewString()
		secondOwnerID := uuid.NewString()
		require.NoError(t, service.BindPublicAsset(t.Context(), Binding{
			AssetID: asset.ID, OwnerType: "post", OwnerID: firstOwnerID, BindingKey: "generated:first",
		}))
		require.NoError(t, service.BindPublicAsset(t.Context(), Binding{
			AssetID: asset.ID, OwnerType: "post", OwnerID: secondOwnerID, BindingKey: "generated:second",
		}))

		require.NoError(t, service.ReleasePublicAssetBindings(t.Context(), "post", firstOwnerID, "generated"))
		requirePublicAssetState(t, db, asset.ID, model.PublicAssetStatusReady, false)
		require.NoError(t, service.ReleasePublicAssetBindings(t.Context(), "post", secondOwnerID, "generated"))
		requirePublicAssetState(t, db, asset.ID, model.PublicAssetStatusDeletePending, true)
	})

	t.Run("replacement preserves reusable source projections", func(t *testing.T) {
		db := newUnitDB(t)
		now := time.Date(2026, time.August, 4, 6, 0, 0, 0, time.UTC)
		service := NewLifecycle(db, "")
		service.now = func() time.Time { return now }
		oldAsset := allocateAndCompleteTestPublicAsset(t, service, validPublicAssetAllocation())
		newAsset := allocateAndCompleteTestPublicAsset(t, service, validPublicAssetAllocation())
		ownerID := uuid.NewString()
		binding := Binding{
			AssetID: oldAsset.ID, OwnerType: "page", OwnerID: ownerID, BindingKey: "featured_image",
		}
		require.NoError(t, service.BindPublicAsset(t.Context(), binding))
		now = now.Add(time.Minute)
		binding.AssetID = newAsset.ID
		require.NoError(t, service.BindPublicAsset(t.Context(), binding))

		requirePublicAssetState(t, db, oldAsset.ID, model.PublicAssetStatusReady, false)
		requirePublicAssetState(t, db, newAsset.ID, model.PublicAssetStatusReady, false)
		var retired model.PublicAsset
		require.NoError(t, db.Where("id = ?", oldAsset.ID).Take(&retired).Error)
		require.Nil(t, retired.DeleteRequestedAt)
		var storedBinding model.PublicAssetBinding
		require.NoError(t, db.Where(
			"owner_type = ? AND owner_id = ? AND binding_key = ?", binding.OwnerType, binding.OwnerID, binding.BindingKey,
		).Take(&storedBinding).Error)
		require.Equal(t, newAsset.ID, storedBinding.AssetID)

		binding.AssetID = oldAsset.ID
		require.NoError(t, service.BindPublicAsset(t.Context(), binding))
		require.NoError(t, db.Where(
			"owner_type = ? AND owner_id = ? AND binding_key = ?", binding.OwnerType, binding.OwnerID, binding.BindingKey,
		).Take(&storedBinding).Error)
		require.Equal(t, oldAsset.ID, storedBinding.AssetID)
		requirePublicAssetState(t, db, newAsset.ID, model.PublicAssetStatusReady, false)
	})

	t.Run("prefix release changes exact affected assets", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		canonical := allocateAndCompleteTestPublicAsset(t, service, validGeneratedPublicAssetAllocation())
		localized := allocateAndCompleteTestPublicAsset(t, service, validGeneratedPublicAssetAllocation())
		featured := allocateAndCompleteTestPublicAsset(t, service, validPublicAssetAllocation())
		ownerID := uuid.NewString()
		for _, binding := range []Binding{
			{AssetID: canonical.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "og"},
			{AssetID: localized.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "og:ko"},
			{AssetID: featured.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "featured_image"},
		} {
			require.NoError(t, service.BindPublicAsset(t.Context(), binding))
		}

		require.NoError(t, service.ReleasePublicAssetBindings(t.Context(), "post", ownerID, "og"))
		requirePublicAssetState(t, db, canonical.ID, model.PublicAssetStatusDeletePending, true)
		requirePublicAssetState(t, db, localized.ID, model.PublicAssetStatusDeletePending, true)
		requirePublicAssetState(t, db, featured.ID, model.PublicAssetStatusReady, false)
		var bindingCount int64
		require.NoError(t, db.Model(&model.PublicAssetBinding{}).
			Where("owner_type = ? AND owner_id = ?", "post", ownerID).
			Count(&bindingCount).Error)
		require.Equal(t, int64(1), bindingCount)
	})

	t.Run("exact release preserves sibling localized binding", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		english := allocateAndCompleteTestPublicAsset(t, service, validGeneratedPublicAssetAllocation())
		korean := allocateAndCompleteTestPublicAsset(t, service, validGeneratedPublicAssetAllocation())
		ownerID := uuid.NewString()
		for _, binding := range []Binding{
			{AssetID: english.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "og:en"},
			{AssetID: korean.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "og:ko"},
		} {
			require.NoError(t, service.BindPublicAsset(t.Context(), binding))
		}

		require.NoError(t, service.ReleaseExactPublicAssetBindings(
			t.Context(), "post", ownerID, []string{"og:en"},
		))
		requirePublicAssetState(t, db, english.ID, model.PublicAssetStatusDeletePending, true)
		requirePublicAssetState(t, db, korean.ID, model.PublicAssetStatusReady, false)
		var binding model.PublicAssetBinding
		require.NoError(t, db.Where(
			"owner_type = ? AND owner_id = ? AND binding_key = ?", "post", ownerID, "og:ko",
		).Take(&binding).Error)
		require.Equal(t, korean.ID, binding.AssetID)
	})
}

func TestMediaAssetLifecycleDeletionStateContract(t *testing.T) {
	db := newUnitDB(t)
	now := time.Date(2026, time.August, 4, 7, 0, 0, 0, time.UTC)
	service := NewLifecycle(db, "")
	service.now = func() time.Time { return now }

	allocated := allocateTestPublicAsset(t, service, validPublicAssetAllocation())
	require.Error(t, service.RequestPublicAssetDeletion(t.Context(), allocated.ID))
	requirePublicAssetState(t, db, allocated.ID, model.PublicAssetStatusAllocated, false)

	failed := allocateTestPublicAsset(t, service, validPublicAssetAllocation())
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", failed.ID).
		Update("status", model.PublicAssetStatusFailed).Error)
	require.NoError(t, service.RequestPublicAssetDeletion(t.Context(), failed.ID))
	requirePublicAssetState(t, db, failed.ID, model.PublicAssetStatusDeletePending, true)
	require.NoError(t, service.RequestPublicAssetDeletion(t.Context(), failed.ID))

	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", failed.ID).
		Update("status", model.PublicAssetStatusDeleted).Error)
	require.NoError(t, service.RequestPublicAssetDeletion(t.Context(), failed.ID))
}

func requirePublicAssetState(t *testing.T, db *gorm.DB, assetID string, status string, deletionRequested bool) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Where("id = ?", assetID).Take(&asset).Error)
	require.Equal(t, status, asset.Status)
	if deletionRequested {
		require.NotNil(t, asset.DeleteRequestedAt)
	} else {
		require.Nil(t, asset.DeleteRequestedAt)
	}
}

func TestPublicAssetAllocatorRejectsPendingSourceFile(t *testing.T) {
	t.Parallel()
	db := newUnitDB(t)
	fileID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		"INSERT INTO file (id, delete_requested_at) VALUES (?, ?)", fileID, now,
	).Error)

	_, _, err := NewLifecycle(db, "").AllocatePublicAsset(
		t.Context(),
		Allocation{
			SourceFileID: &fileID, Kind: "thumbnail", Extension: "webp",
			MimeType: "image/webp", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	var assetCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Count(&assetCount).Error)
	require.Zero(t, assetCount)
}

func TestMediaAssetLifecycleGenerationAtomicSwitch(t *testing.T) {
	db := newUnitDB(t)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	service := NewLifecycle(db, "")
	service.now = func() time.Time { return now }
	fileID := uuid.NewString()

	first, firstTarget := seedAllocatedMediaGeneration(t, service, fileID)
	require.Equal(t, "media/"+fileID+"/hls/"+first.ID, firstTarget.GetObjectPrefix())
	firstDigest := sha256.Sum256([]byte("first manifest"))
	firstReady, err := service.CompleteMediaGeneration(t.Context(), &commonv1.MediaGenerationWriteResult{
		GenerationId:   first.ID,
		ManifestSha256: firstDigest[:],
		ObjectCount:    4,
		TotalSize:      1000,
	})
	require.NoError(t, err)
	require.Equal(t, model.MediaGenerationStatusReady, firstReady.Status)

	now = now.Add(time.Minute)
	second, _ := seedAllocatedMediaGeneration(t, service, fileID)
	secondDigest := sha256.Sum256([]byte("second manifest"))
	secondResult := &commonv1.MediaGenerationWriteResult{
		GenerationId:   second.ID,
		ManifestSha256: secondDigest[:],
		ObjectCount:    5,
		TotalSize:      2000,
	}
	secondReady, err := service.CompleteMediaGeneration(t.Context(), secondResult)
	require.NoError(t, err)
	require.Equal(t, model.MediaGenerationStatusReady, secondReady.Status)
	_, err = service.CompleteMediaGeneration(t.Context(), secondResult)
	require.NoError(t, err)

	var retired model.MediaGeneration
	require.NoError(t, db.First(&retired, "id = ?", first.ID).Error)
	require.Equal(t, model.MediaGenerationStatusRetired, retired.Status)
	require.Equal(t, now.Add(retiredMediaGenerationRetention), *retired.DeleteAfter)

	conflicting := proto.Clone(secondResult).(*commonv1.MediaGenerationWriteResult)
	conflicting.TotalSize++
	_, err = service.CompleteMediaGeneration(t.Context(), conflicting)
	require.Error(t, err)
}

func newUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE file (
			id text PRIMARY KEY, file_name text, mime_type text, file_size integer,
			extension text, sha256 blob, duration_seconds integer,
			ingest_slot_id text, ingest_attempt_id text,
			delete_requested_at datetime, created_at datetime
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE public_asset (
			id text PRIMARY KEY, source_file_id text, kind text NOT NULL,
			object_key text NOT NULL UNIQUE, extension text NOT NULL, mime_type text NOT NULL,
			file_size integer, sha256 blob, disposition text NOT NULL, download_filename text,
			status text NOT NULL, ready_at datetime, delete_requested_at datetime, deleted_at datetime,
			failed_at datetime, failure_reason text, created_at datetime NOT NULL, updated_at datetime NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE public_asset_binding (
			asset_id text NOT NULL, owner_type text NOT NULL, owner_id text NOT NULL,
			binding_key text NOT NULL, source_file_id text, created_at datetime NOT NULL,
			updated_at datetime NOT NULL, PRIMARY KEY (owner_type, owner_id, binding_key)
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE media_generation (
			id text PRIMARY KEY, file_id text NOT NULL, kind text NOT NULL,
			object_prefix text NOT NULL UNIQUE, manifest_name text NOT NULL,
			manifest_sha256 blob, object_count integer, total_size integer, status text NOT NULL,
			ready_at datetime, retired_at datetime, delete_after datetime,
			created_at datetime NOT NULL, updated_at datetime NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE file_ingest_binding (
			file_id text PRIMARY KEY, upload_type text NOT NULL, entity_type text,
			entity_id text NOT NULL, created_at datetime NOT NULL
		)
	`).Error)
	return db
}

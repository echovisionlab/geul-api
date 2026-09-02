package mediaasset

import (
	"crypto/sha256"
	"errors"
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestNewMediaAssetLifecycleServiceRequiresDatabase(t *testing.T) {
	require.Panics(t, func() {
		NewLifecycle(nil, "")
	})
}

func TestValidatePublicAssetAllocation(t *testing.T) {
	valid := Allocation{
		Kind:        "image",
		Extension:   "webp",
		MimeType:    "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
	require.NoError(t, validatePublicAssetAllocation(valid))

	for name, mutate := range map[string]func(*Allocation){
		"unknown kind":       func(input *Allocation) { input.Kind = "unknown" },
		"missing extension":  func(input *Allocation) { input.Extension = "" },
		"unknown MIME":       func(input *Allocation) { input.MimeType = "application/x-unknown"; input.Extension = "bin" },
		"extension mismatch": func(input *Allocation) { input.Extension = "png" },
		"invalid source ID": func(input *Allocation) {
			input.SourceFileID = new("not-a-uuid")
		},
		"inline filename": func(input *Allocation) {
			input.DownloadFilename = new("image.webp")
		},
		"attachment without filename": func(input *Allocation) {
			input.Disposition = commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT
		},
		"unspecified disposition": func(input *Allocation) {
			input.Disposition = commonv1.AssetDisposition_ASSET_DISPOSITION_UNSPECIFIED
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			require.Error(t, validatePublicAssetAllocation(input))
		})
	}

	attachment := valid
	attachment.Disposition = commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT
	attachment.DownloadFilename = new(" image.webp ")
	require.Error(t, validatePublicAssetAllocation(attachment))
}

func TestValidatePublicAssetAllocationEnforcesKindMediaContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      string
		extension string
		mimeType  string
		wantError bool
	}{
		{name: "image webp", kind: "image", extension: "webp", mimeType: "image/webp"},
		{name: "image rejects PDF", kind: "image", extension: "pdf", mimeType: "application/pdf", wantError: true},
		{name: "mesh GLB", kind: "mesh", extension: "glb", mimeType: "model/gltf-binary"},
		{name: "mesh rejects image", kind: "mesh", extension: "webp", mimeType: "image/webp", wantError: true},
		{name: "waveform JSON", kind: "waveform", extension: "json", mimeType: "application/json"},
		{name: "waveform rejects PNG", kind: "waveform", extension: "png", mimeType: "image/png", wantError: true},
		{name: "spectrogram PNG", kind: "spectrogram", extension: "png", mimeType: "image/png"},
		{name: "thumbnail WebP", kind: "thumbnail", extension: "webp", mimeType: "image/webp"},
		{name: "thumbnail rejects PNG", kind: "thumbnail", extension: "png", mimeType: "image/png", wantError: true},
		{name: "avatar rejects PNG", kind: "avatar", extension: "png", mimeType: "image/png", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePublicAssetAllocation(Allocation{
				Kind:        tc.kind,
				Extension:   tc.extension,
				MimeType:    tc.mimeType,
				Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMediaAssetLifecycleAllocationAndCompletionErrors(t *testing.T) {
	t.Run("canonical key validation", func(t *testing.T) {
		const mimeType = "application/x-invalid-asset-extension"
		model.MimeToExtension[mimeType] = "bad.ext"
		t.Cleanup(func() { delete(model.MimeToExtension, mimeType) })

		_, _, err := NewLifecycle(newUnitDB(t), "").AllocatePublicAsset(
			t.Context(),
			Allocation{
				Kind: "image", Extension: "bad.ext", MimeType: mimeType,
				Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
			},
		)
		require.Error(t, err)
	})

	t.Run("allocation database failure", func(t *testing.T) {
		db := newUnitDB(t)
		require.NoError(t, db.Exec("DROP TABLE public_asset").Error)
		_, _, err := NewLifecycle(db, "").AllocatePublicAsset(t.Context(), validPublicAssetAllocation())
		require.Error(t, err)
	})

	t.Run("completion validation", func(t *testing.T) {
		service := NewLifecycle(newUnitDB(t), "")
		_, err := service.CompletePublicAsset(t.Context(), nil)
		require.Error(t, err)
		_, err = service.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
			AssetId: "invalid", FileSize: 1, Sha256: make([]byte, 32),
		})
		require.Error(t, err)
		_, err = service.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
			AssetId: uuid.NewString(), FileSize: 1, Sha256: []byte{1},
		})
		require.Error(t, err)
		_, err = service.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
			AssetId: uuid.NewString(), FileSize: 1, Sha256: make([]byte, 32),
		})
		require.Error(t, err)
	})

	t.Run("non allocated status", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		asset := allocateTestPublicAsset(t, service, validPublicAssetAllocation())
		require.NoError(t, db.Model(&model.PublicAsset{}).
			Where("id = ?", asset.ID).
			Update("status", model.PublicAssetStatusFailed).Error)
		_, err := service.CompletePublicAsset(t.Context(), validAssetWriteResult(asset.ID))
		require.Error(t, err)
	})

	t.Run("update rollback", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		asset := allocateTestPublicAsset(t, service, validPublicAssetAllocation())
		require.NoError(t, db.Exec(`
			CREATE TRIGGER reject_public_asset_update BEFORE UPDATE ON public_asset
			BEGIN SELECT RAISE(FAIL, 'reject update'); END
		`).Error)
		_, err := service.CompletePublicAsset(t.Context(), validAssetWriteResult(asset.ID))
		require.Error(t, err)
	})
}

func TestMediaAssetLifecycleReadyAssetAndBindingErrors(t *testing.T) {
	db := newUnitDB(t)
	service := NewLifecycle(db, "cdn.example.com")
	asset := allocateTestPublicAsset(t, service, validPublicAssetAllocation())

	_, err := service.ReadyAssetRef(t.Context(), asset.ID)
	require.Error(t, err)
	_, err = service.ReadyAssetRef(t.Context(), uuid.NewString())
	require.Error(t, err)
	_, err = service.ReadyAssetRefForSourceFile(t.Context(), uuid.NewString(), "image")
	require.Error(t, err)
	require.Error(t, service.BindPublicAsset(t.Context(), Binding{}))
	require.Error(t, service.BindPublicAsset(t.Context(), Binding{
		AssetID: asset.ID, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "image",
	}))

	_, err = service.CompletePublicAsset(t.Context(), validAssetWriteResult(asset.ID))
	require.NoError(t, err)
	ref, err := ReadyPublicAssetRefForSourceFile(t.Context(), db, "cdn.example.com", *asset.SourceFileID, "image")
	require.NoError(t, err)
	require.Equal(t, asset.ID, ref.GetAssetId())
	_, err = ReadyPublicAssetRefForSourceFile(t.Context(), db, "cdn.example.com", *asset.SourceFileID, "logo")
	require.Error(t, err)
	_, err = service.ReadyAssetRefForSourceFile(t.Context(), *asset.SourceFileID, "unsupported")
	require.Error(t, err)
	_, err = service.BindReadyAssetForSourceFile(t.Context(), uuid.NewString(), "post", uuid.NewString(), "image", "image")
	require.Error(t, err)
	_, err = service.BindReadyAssetForSourceFile(t.Context(), *asset.SourceFileID, "", uuid.NewString(), "image", "image")
	require.Error(t, err)

	invalidAsset := model.PublicAsset{
		ID: "invalid", Extension: "webp", FileSize: new(int64(1)), SHA256: make([]byte, 32),
	}
	_, err = service.AssetRef(invalidAsset)
	require.Error(t, err)

	legacyAttachmentID := uuid.NewString()
	size := int64(1)
	now := service.now().UTC()
	require.NoError(t, db.Create(&model.PublicAsset{
		ID:               legacyAttachmentID,
		Kind:             "attachment",
		ObjectKey:        "asset/" + legacyAttachmentID + ".pdf",
		Extension:        "pdf",
		MimeType:         "application/pdf",
		FileSize:         &size,
		SHA256:           make([]byte, 32),
		Disposition:      "attachment",
		DownloadFilename: new("document.pdf"),
		Status:           model.PublicAssetStatusReady,
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error)
	_, err = service.ReadyAssetRef(t.Context(), legacyAttachmentID)
	require.ErrorContains(t, err, "cannot be emitted")
}

func TestMediaAssetLifecycleBindingDatabaseErrors(t *testing.T) {
	t.Run("ready count", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		require.NoError(t, db.Exec("DROP TABLE public_asset").Error)
		require.Error(t, service.BindPublicAsset(t.Context(), Binding{
			AssetID: uuid.NewString(), OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "image",
		}))
	})

	t.Run("binding insert", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		asset := allocateAndCompleteTestPublicAsset(t, service, validPublicAssetAllocation())
		require.NoError(t, db.Exec("DROP TABLE public_asset_binding").Error)
		require.Error(t, service.BindPublicAsset(t.Context(), Binding{
			AssetID: asset.ID, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "image",
		}))
	})
}

func TestMediaAssetLifecycleDeletionAndReleaseErrors(t *testing.T) {
	t.Run("invalid release input", func(t *testing.T) {
		service := NewLifecycle(newUnitDB(t), "")
		require.Error(t, service.ReleasePublicAssetBindings(t.Context(), "", "owner", "image"))
	})

	t.Run("binding lookup failure", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		require.NoError(t, db.Exec("DROP TABLE public_asset_binding").Error)
		require.Error(t, service.RequestPublicAssetDeletion(t.Context(), uuid.NewString()))
		require.Error(t, service.ReleasePublicAssetBindings(t.Context(), "post", uuid.NewString(), "image"))
	})

	t.Run("invalid state", func(t *testing.T) {
		service := NewLifecycle(newUnitDB(t), "")
		asset := allocateTestPublicAsset(t, service, validPublicAssetAllocation())
		require.Error(t, service.RequestPublicAssetDeletion(t.Context(), asset.ID))
	})

	t.Run("update failure", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		asset := allocateAndCompleteTestPublicAsset(t, service, validPublicAssetAllocation())
		require.NoError(t, db.Exec(`
			CREATE TRIGGER reject_public_asset_delete BEFORE UPDATE ON public_asset
			BEGIN SELECT RAISE(FAIL, 'reject delete'); END
		`).Error)
		require.Error(t, service.RequestPublicAssetDeletion(t.Context(), asset.ID))
	})
}

func TestMediaAssetLifecycleGenerationErrors(t *testing.T) {
	t.Run("completion validation", func(t *testing.T) {
		service := NewLifecycle(newUnitDB(t), "")
		_, err := service.CompleteMediaGeneration(t.Context(), nil)
		require.Error(t, err)
		_, err = service.CompleteMediaGeneration(t.Context(), &commonv1.MediaGenerationWriteResult{GenerationId: "invalid"})
		require.Error(t, err)
		_, err = service.CompleteMediaGeneration(t.Context(), &commonv1.MediaGenerationWriteResult{
			GenerationId: uuid.NewString(), ManifestSha256: []byte{1}, ObjectCount: 1, TotalSize: 1,
		})
		require.Error(t, err)
		_, err = service.CompleteMediaGeneration(t.Context(), validMediaGenerationWriteResult(uuid.NewString()))
		require.Error(t, err)
	})

	t.Run("non allocated status", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		generation, _ := seedAllocatedMediaGeneration(t, service, uuid.NewString())
		require.NoError(t, db.Model(&model.MediaGeneration{}).
			Where("id = ?", generation.ID).
			Update("status", model.MediaGenerationStatusRetired).Error)
		_, err := service.CompleteMediaGeneration(t.Context(), validMediaGenerationWriteResult(generation.ID))
		require.Error(t, err)
	})

	t.Run("ready update failure", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		generation, _ := seedAllocatedMediaGeneration(t, service, uuid.NewString())
		require.NoError(t, db.Exec(`
			CREATE TRIGGER reject_generation_ready BEFORE UPDATE ON media_generation
			WHEN OLD.status = 'allocated'
			BEGIN SELECT RAISE(FAIL, 'reject ready'); END
		`).Error)
		_, err := service.CompleteMediaGeneration(t.Context(), validMediaGenerationWriteResult(generation.ID))
		require.Error(t, err)
	})

	t.Run("retirement update failure", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		fileID := uuid.NewString()
		first, _ := seedAllocatedMediaGeneration(t, service, fileID)
		_, err := service.CompleteMediaGeneration(t.Context(), validMediaGenerationWriteResult(first.ID))
		require.NoError(t, err)
		second, _ := seedAllocatedMediaGeneration(t, service, fileID)
		require.NoError(t, db.Exec(`
			CREATE TRIGGER reject_generation_retire BEFORE UPDATE ON media_generation
			WHEN OLD.status = 'ready'
			BEGIN SELECT RAISE(FAIL, 'reject retire'); END
		`).Error)
		_, err = service.CompleteMediaGeneration(t.Context(), validMediaGenerationWriteResult(second.ID))
		require.Error(t, err)
	})

	t.Run("post completion read failure", func(t *testing.T) {
		db := newUnitDB(t)
		service := NewLifecycle(db, "")
		generation, _ := seedAllocatedMediaGeneration(t, service, uuid.NewString())
		queryCount := 0
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register(
			"test:fail_generation_reload",
			func(tx *gorm.DB) {
				queryCount++
				if queryCount == 2 {
					tx.AddError(errors.New("reject generation reload"))
				}
			},
		))
		_, err := service.CompleteMediaGeneration(t.Context(), validMediaGenerationWriteResult(generation.ID))
		require.Error(t, err)
	})
}

func TestMediaAssetLifecycleDatabaseReadErrors(t *testing.T) {
	db := newUnitDB(t)
	service := NewLifecycle(db, "")
	require.NoError(t, db.Exec("DROP TABLE public_asset").Error)
	_, err := service.ReadyAssetRef(t.Context(), uuid.NewString())
	require.Error(t, err)
	_, err = service.ReadyAssetRefForSourceFile(t.Context(), uuid.NewString(), "image")
	require.Error(t, err)
}

func TestMediaAssetLifecycleValueHelpers(t *testing.T) {
	require.Nil(t, normalizedOptionalString(nil))
	require.Nil(t, normalizedOptionalString(new("  ")))
	require.Equal(t, "value", *normalizedOptionalString(new(" value ")))
	require.Equal(t, "image/webp", canonicalMimeType(" image/webp; charset=binary "))
}

func validPublicAssetAllocation() Allocation {
	sourceFileID := uuid.NewString()
	return Allocation{
		SourceFileID: &sourceFileID,
		Kind:         "image",
		Extension:    "webp",
		MimeType:     "image/webp",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

func validGeneratedPublicAssetAllocation() Allocation {
	return Allocation{
		Kind:        "image",
		Extension:   "webp",
		MimeType:    "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

func allocateTestPublicAsset(
	t *testing.T,
	service *Lifecycle,
	input Allocation,
) *model.PublicAsset {
	t.Helper()
	if input.SourceFileID != nil {
		require.NoError(t, service.db.Exec(
			"INSERT OR IGNORE INTO file (id, delete_requested_at) VALUES (?, NULL)",
			*input.SourceFileID,
		).Error)
	}
	asset, _, err := service.AllocatePublicAsset(t.Context(), input)
	require.NoError(t, err)
	return asset
}

func allocateAndCompleteTestPublicAsset(
	t *testing.T,
	service *Lifecycle,
	input Allocation,
) *model.PublicAsset {
	t.Helper()
	asset := allocateTestPublicAsset(t, service, input)
	ready, err := service.CompletePublicAsset(t.Context(), validAssetWriteResult(asset.ID))
	require.NoError(t, err)
	return ready
}

func validAssetWriteResult(assetID string) *commonv1.AssetWriteResult {
	digest := sha256.Sum256([]byte(assetID))
	return &commonv1.AssetWriteResult{AssetId: assetID, FileSize: 1024, Sha256: digest[:]}
}

func validMediaGenerationWriteResult(generationID string) *commonv1.MediaGenerationWriteResult {
	digest := sha256.Sum256([]byte(generationID))
	return &commonv1.MediaGenerationWriteResult{
		GenerationId: generationID, ManifestSha256: digest[:], ObjectCount: 4, TotalSize: 4096,
	}
}

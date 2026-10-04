package filemedia

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// The caller holds the completion advisory lock throughout allocation, copy,
// verification and registration. Both database phases recheck fresh authority.
func (s *FileService) registerVerifiedClientMediaBundle(ctx context.Context, completion *multipartCompletion, authorize func(context.Context, *gorm.DB) error) error {
	session := completion.session
	if session.ClientMediaBundleID == nil {
		return nil
	}
	ready, err := s.clientMediaRegistrationReady(ctx, session)
	if err != nil || ready {
		return err
	}
	var plan clientMediaPlan
	if completion.clientMediaPlan != nil {
		plan = *completion.clientMediaPlan
	} else {
		plan, err = s.verifyClientMediaBundle(ctx, session)
		if err != nil {
			return err
		}
	}
	// Allocate owned rows first so a failed/revoked copy leaves cleanup authority.
	if err := s.withClientMediaFileLock(ctx, session.FileID, authorize, func(tx *gorm.DB) error {
		return allocateClientMedia(ctx, tx, session.FileID, plan.Artifacts)
	}); err != nil {
		return err
	}
	result, err := s.copyClientMedia(ctx, session.FileID, *session.ClientMediaBundleID, plan.Artifacts)
	if err != nil {
		return err
	}
	return s.withClientMediaFileLock(ctx, session.FileID, authorize, func(tx *gorm.DB) error {
		return s.completeClientMedia(ctx, tx, session, plan, result)
	})
}

func (s *FileService) withClientMediaFileLock(ctx context.Context, fileID string, authorize func(context.Context, *gorm.DB) error, apply func(*gorm.DB) error) error {
	if authorize == nil {
		return fmt.Errorf("client media authority fence required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := authorize(ctx, tx); err != nil {
			return err
		}
		if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{fileID}); err != nil {
			return err
		}
		return apply(tx)
	})
}

func allocateClientMedia(ctx context.Context, tx *gorm.DB, fileID string, artifacts []clientMediaArtifact) error {
	_, allocated, err := ensureStableFileIngestMediaGeneration(ctx, tx, fileID)
	if err != nil {
		return err
	}
	if !allocated {
		return fmt.Errorf("client HLS target is already sealed")
	}
	for _, artifact := range artifacts {
		if artifact.isHLS() {
			continue
		}
		_, allocated, err := ensureStableFileIngestPublicAsset(ctx, tx, fileID, artifact.assetKind(), artifact.extension(), artifact.MimeType)
		if err != nil {
			return err
		}
		if !allocated {
			return fmt.Errorf("client derivative target is already sealed")
		}
	}
	return nil
}

func (artifact clientMediaArtifact) canonicalKey(fileID, generationID string) string {
	if artifact.isHLS() {
		return "media/" + fileID + "/hls/" + generationID + "/" + artifact.Path
	}
	return "asset/" + stableFileIngestFinalizerID(fileID, artifact.assetKind()) + "." + artifact.extension()
}

// Browser artifacts can write staging only. Copy to server-owned canonical
// destinations, then independently verify every copied object's bytes again.
func (s *FileService) copyClientMedia(ctx context.Context, fileID, bundleID string, artifacts []clientMediaArtifact) (*commonv1.MediaGenerationWriteResult, error) {
	result := &commonv1.MediaGenerationWriteResult{GenerationId: stableFileIngestFinalizerID(fileID, "hls")}
	for _, artifact := range artifacts {
		if artifact.isHLS() {
			result.TotalSize += artifact.Size
			result.ObjectCount++
			if artifact.Path == "master.m3u8" {
				digest, err := artifact.digest()
				if err != nil {
					return nil, err
				}
				result.ManifestSha256 = digest
			}
		}
		if err := s.copyClientArtifact(ctx, fileID, bundleID, result.GenerationId, artifact); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *FileService) copyClientArtifact(ctx context.Context, fileID, bundleID, generationID string, artifact clientMediaArtifact) error {
	key := artifact.canonicalKey(fileID, generationID)
	source := s.s3Bucket + "/" + clientMediaStagingKey(bundleID, artifact.Path)
	_, err := s.s3Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(key), CopySource: aws.String(url.PathEscape(source)),
	})
	if err != nil {
		if isMissingStoredObjectError(err) {
			return missingClientArtifact(artifact.Path)
		}
		return fmt.Errorf("copy client artifact %s: %w", artifact.Path, err)
	}
	if _, err := s.verifyClientMediaObject(ctx, key, artifact); err != nil {
		return fmt.Errorf("verify canonical artifact: %w", err)
	}
	return nil
}

func (s *FileService) completeClientMedia(ctx context.Context, tx *gorm.DB, session model.UploadSession, plan clientMediaPlan, result *commonv1.MediaGenerationWriteResult) error {
	lifecycle := mediaasset.NewLifecycle(tx, s.cdnDomain)
	if _, _, err := ensureStableFileIngestMediaGeneration(ctx, tx, session.FileID); err != nil {
		return err
	}
	if _, err := lifecycle.CompleteMediaGeneration(ctx, result); err != nil {
		return err
	}
	if err := storeClientMediaDerivative(ctx, tx, session.FileID, clientHLS, nil, &result.GenerationId); err != nil {
		return err
	}
	for _, artifact := range plan.Artifacts {
		if artifact.isHLS() {
			continue
		}
		target, _, err := ensureStableFileIngestPublicAsset(ctx, tx, session.FileID, artifact.assetKind(), artifact.extension(), artifact.MimeType)
		if err != nil {
			return err
		}
		digest, err := artifact.digest()
		if err != nil {
			return err
		}
		if _, err := lifecycle.CompletePublicAsset(ctx, &commonv1.AssetWriteResult{AssetId: target.GetAssetId(), FileSize: artifact.Size, Sha256: digest}); err != nil {
			return err
		}
		assetID := target.GetAssetId()
		if err := storeClientMediaDerivative(ctx, tx, session.FileID, managev1.FileDerivativeType(artifact.DerivativeType), &assetID, nil); err != nil {
			return err
		}
	}
	return tx.Model(&model.File{}).Where("id = ? AND delete_requested_at IS NULL", session.FileID).Updates(structured.Fields{
		"duration_seconds": int(math.Ceil(plan.DurationSeconds)), "client_media_bundle_id": *session.ClientMediaBundleID,
	}).Error
}

func storeClientMediaDerivative(ctx context.Context, tx *gorm.DB, fileID string, kind managev1.FileDerivativeType, asset, generation *string) error {
	return tx.WithContext(ctx).Table("file_derivative").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}, {Name: "type"}},
		DoUpdates: clause.Assignments(structured.Fields{"asset_id": asset, "media_generation_id": generation}),
	}).Create(structured.Fields{
		"id": uuid.NewString(), "file_id": fileID, "type": kind.String(), "asset_id": asset,
		"media_generation_id": generation, "created_at": time.Now().UTC(),
	}).Error
}

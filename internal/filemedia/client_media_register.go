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

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// The caller holds the completion advisory lock throughout allocation, copy,
// verification and registration. Both database phases recheck fresh authority.
func (s *Service) registerVerifiedClientMediaBundle(ctx context.Context, completion *multipartCompletion, authorize func(context.Context, Store, func(*auth.UserInfo) (bool, error)) error) error {
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
	if err := s.withClientMediaFileLock(ctx, session.FileID, authorize, func(store Store) error {
		return allocateClientMedia(ctx, store, session.FileID, plan.Artifacts)
	}); err != nil {
		return err
	}
	result, err := s.copyClientMedia(ctx, session.FileID, *session.ClientMediaBundleID, plan.Artifacts)
	if err != nil {
		return err
	}
	return s.withClientMediaFileLock(ctx, session.FileID, authorize, func(store Store) error {
		return s.completeClientMedia(ctx, store, session, plan, result)
	})
}

func (s *Service) withClientMediaFileLock(ctx context.Context, fileID string, authorize func(context.Context, Store, func(*auth.UserInfo) (bool, error)) error, apply func(Store) error) error {
	if authorize == nil {
		return fmt.Errorf("client media authority fence required")
	}
	_, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[struct{}, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			return authorize(ctx, store, lockActive)
		}),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			if err := store.LockIngestFile(ctx, fileID); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, apply(store)
		},
		Audit: func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func allocateClientMedia(ctx context.Context, store Store, fileID string, artifacts []clientMediaArtifact) error {
	_, allocated, err := ensureStableFileIngestMediaGeneration(ctx, store, fileID)
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
		_, allocated, err := ensureStableFileIngestPublicAsset(ctx, store, fileID, artifact.assetKind(), artifact.extension(), artifact.MimeType)
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
func (s *Service) copyClientMedia(ctx context.Context, fileID, bundleID string, artifacts []clientMediaArtifact) (*mediaasset.WrittenGeneration, error) {
	result := &mediaasset.WrittenGeneration{GenerationID: stableFileIngestFinalizerID(fileID, "hls")}
	for _, artifact := range artifacts {
		if artifact.isHLS() {
			result.TotalSize += artifact.Size
			result.ObjectCount++
			if artifact.Path == "master.m3u8" {
				digest, err := artifact.digest()
				if err != nil {
					return nil, err
				}
				result.ManifestSHA256 = digest
			}
		}
		if err := s.copyClientArtifact(ctx, fileID, bundleID, result.GenerationID, artifact); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Service) copyClientArtifact(ctx context.Context, fileID, bundleID, generationID string, artifact clientMediaArtifact) error {
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

func (s *Service) completeClientMedia(ctx context.Context, store Store, session model.UploadSession, plan clientMediaPlan, result *mediaasset.WrittenGeneration) error {
	if _, _, err := ensureStableFileIngestMediaGeneration(ctx, store, session.FileID); err != nil {
		return err
	}
	if err := store.CompleteIngestGeneration(ctx, s.cdnDomain, *result); err != nil {
		return err
	}
	if err := storeClientMediaDerivative(ctx, store, session.FileID, clientHLS, nil, &result.GenerationID); err != nil {
		return err
	}
	for _, artifact := range plan.Artifacts {
		if artifact.isHLS() {
			continue
		}
		target, _, err := ensureStableFileIngestPublicAsset(ctx, store, session.FileID, artifact.assetKind(), artifact.extension(), artifact.MimeType)
		if err != nil {
			return err
		}
		digest, err := artifact.digest()
		if err != nil {
			return err
		}
		if err := store.CompleteIngestAsset(ctx, s.cdnDomain, mediaasset.WrittenAsset{AssetID: target.AssetID, FileSize: artifact.Size, SHA256: digest}); err != nil {
			return err
		}
		assetID := target.AssetID
		if err := storeClientMediaDerivative(ctx, store, session.FileID, managev1.FileDerivativeType(artifact.DerivativeType), &assetID, nil); err != nil {
			return err
		}
	}
	return store.SetClientMediaBundle(ctx, session.FileID, *session.ClientMediaBundleID, int(math.Ceil(plan.DurationSeconds)))
}

func storeClientMediaDerivative(ctx context.Context, store Store, fileID string, kind managev1.FileDerivativeType, asset, generation *string) error {
	return store.UpsertIngestDerivative(ctx, domain.IngestDerivative{ID: uuid.NewString(), FileID: fileID, Type: kind.String(), AssetID: asset, GenerationID: generation, CreatedAt: time.Now().UTC()})
}

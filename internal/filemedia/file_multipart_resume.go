package filemedia

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// multipartResumeReconciliation carries the current lifecycle state and the
// corresponding authoritative or frozen part snapshot.
type multipartResumeReconciliation struct {
	session model.UploadSession
	parts   []*managev1.UploadPartInfo
}

func (s *Service) prepareMultipartResumeResponse(
	ctx context.Context,
	session model.UploadSession,
	response *managev1.InitiateMultipartUploadResponse,
) (*managev1.InitiateMultipartUploadResponse, error) {
	if session.Status != model.UploadSessionStatusFinalizing {
		reconciled, err := s.reconcileMultipartResumeResponse(ctx, session, response)
		if err != nil {
			return nil, err
		}
		session = reconciled
	}
	if session.Status == model.UploadSessionStatusFinalizing {
		return response, nil
	}
	if err := s.publishMultipartResumeProgress(ctx, session, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Service) reconcileMultipartResumeResponse(
	ctx context.Context,
	session model.UploadSession,
	response *managev1.InitiateMultipartUploadResponse,
) (model.UploadSession, error) {
	if s.testBeforeResumeReconciliation != nil {
		s.testBeforeResumeReconciliation(session)
	}
	reconciliation, err := s.reconcileWritableMultipartResumeParts(ctx, session)
	if err != nil {
		return model.UploadSession{}, fmt.Errorf("failed to reconcile multipart resume parts: %w", err)
	}
	response.Status = uploadSessionStatusToProto(reconciliation.session.Status)
	response.UploadedParts = reconciliation.parts
	return reconciliation.session, nil
}

func (s *Service) publishMultipartResumeProgress(
	ctx context.Context,
	session model.UploadSession,
	response *managev1.InitiateMultipartUploadResponse,
) error {
	emitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(session.EntityType),
		session.EntityID,
		"",
		session.FileID,
		session.FileSize,
	)
	if emitter == nil {
		return nil
	}
	if err := s.bindUploadSessionIngestEmitter(emitter, session); err != nil {
		return err
	}
	progress := int32(0)
	if session.TotalParts > 0 {
		progress = int32(len(response.GetUploadedParts()) * 100 / int(session.TotalParts))
	}
	emitter.publishUploading(progress, nil)
	return nil
}

// reconcileWritableMultipartResumeParts reads the object store's actual
// multipart inventory under the same session advisory authority as part writes
// and completion. The database session remains the authorization/lifecycle
// authority; upload_part rows are reconciled as a completion cache only after
// the S3 inventory has been validated against that session's immutable shape.
func (s *Service) reconcileWritableMultipartResumeParts(
	ctx context.Context,
	session model.UploadSession,
) (multipartResumeReconciliation, error) {
	result := multipartResumeReconciliation{session: session}
	var parts []model.UploadPart
	err := withMultipartCompletionAdvisoryLock(
		ctx,
		s.db,
		session.UploadID,
		session.FileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			current, cachedParts, err := lockedService.loadMultipartResumeSessionState(ctx, session)
			if err != nil {
				return fmt.Errorf("recheck multipart resume session: %w", err)
			}
			result.session = current
			if current.Status == model.UploadSessionStatusFinalizing {
				parts = cachedParts
				return nil
			}

			objectKey, err := uploadSessionObjectKey(session)
			if err != nil {
				return fmt.Errorf("resolve multipart resume object key: %w", err)
			}
			parts, err = s.listAndValidateMultipartResumeParts(ctx, session, objectKey)
			if err != nil {
				return err
			}

			if err := lockedService.reconcileMultipartResumePartCache(ctx, session, parts); err != nil {
				return fmt.Errorf("reconcile multipart resume part cache: %w", err)
			}
			return nil
		},
	)
	if err != nil {
		return multipartResumeReconciliation{}, err
	}
	result.parts = uploadPartInfos(parts)
	return result, nil
}

func (s *Service) loadMultipartResumeSessionState(
	ctx context.Context,
	expected model.UploadSession,
) (model.UploadSession, []model.UploadPart, error) {
	var current model.UploadSession
	var cachedParts []model.UploadPart
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		row, err := store.UploadSession(ctx, expected.UploadID, expected.FileID, true)
		if errors.Is(err, domain.ErrUploadSessionNotFound) {
			return domainerr.Precondition("upload session changed before multipart resume reconciliation")
		}
		if err != nil {
			return err
		}
		current = uploadSessionFromStore(row)
		if !sameMultipartResumeSessionShape(current, expected) {
			return domainerr.Precondition("upload session shape changed before multipart resume reconciliation")
		}
		switch current.Status {
		case model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading:
			return nil
		case model.UploadSessionStatusFinalizing:
			rows, err := store.UploadParts(ctx, expected.UploadID)
			if err != nil {
				return err
			}
			cachedParts = make([]model.UploadPart, len(rows))
			for i, r := range rows {
				cachedParts[i] = model.UploadPart(r)
			}
			return nil
		default:
			return domainerr.Precondition("upload session is no longer resumable")
		}
	})
	return current, cachedParts, err
}

func (s *Service) listAndValidateMultipartResumeParts(
	ctx context.Context,
	session model.UploadSession,
	objectKey string,
) ([]model.UploadPart, error) {
	paginator := s3.NewListPartsPaginator(s.s3Client, &s3.ListPartsInput{
		Bucket:   aws.String(s.s3Bucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(session.UploadID),
	}, func(options *s3.ListPartsPaginatorOptions) {
		// The explicit marker validation below turns a repeated token into an
		// error; this SDK guard is defense in depth against an accidental loop.
		options.StopOnDuplicateToken = true
	})

	now := time.Now()
	parts := make([]model.UploadPart, 0, session.TotalParts)
	seen := make(map[int32]struct{}, session.TotalParts)
	previousMarker := ""
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			// Do not fall back to stale upload_part rows: S3 owns the actual
			// multipart bytes and ETags for a writable resume.
			return nil, fmt.Errorf("list multipart resume parts: %w", err)
		}
		for _, listed := range page.Parts {
			if listed.PartNumber == nil || listed.Size == nil || listed.ETag == nil {
				return nil, domainerr.Precondition("object-store multipart inventory is missing required part identity")
			}
			partNumber := *listed.PartNumber
			if _, duplicate := seen[partNumber]; duplicate {
				return nil, domainerr.Precondition("object-store multipart inventory contains duplicate part numbers")
			}
			seen[partNumber] = struct{}{}

			etag := strings.Trim(strings.TrimSpace(*listed.ETag), "\"")
			expectedSize := expectedMultipartPartSize(
				session.FileSize,
				multipartSessionChunkSize(session),
				partNumber,
			)
			if partNumber <= 0 || partNumber > session.TotalParts || etag == "" || expectedSize <= 0 || *listed.Size != expectedSize {
				return nil, domainerr.Precondition(fmt.Sprintf("object-store multipart part %d does not match the upload session shape", partNumber))
			}
			parts = append(parts, model.UploadPart{
				UploadID:   session.UploadID,
				PartNumber: partNumber,
				ETag:       etag,
				Size:       *listed.Size,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		previousMarker, err = nextMultipartListPartsMarker(page, previousMarker)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

func nextMultipartListPartsMarker(page *s3.ListPartsOutput, previous string) (string, error) {
	if page == nil || !aws.ToBool(page.IsTruncated) {
		return previous, nil
	}
	next := strings.TrimSpace(aws.ToString(page.NextPartNumberMarker))
	if next == "" || next == previous {
		return "", domainerr.Precondition("object-store multipart inventory returned an invalid pagination marker")
	}
	return next, nil
}

func (s *Service) reconcileMultipartResumePartCache(
	ctx context.Context,
	expected model.UploadSession,
	parts []model.UploadPart,
) error {
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		row, err := store.WritableUploadSession(ctx, expected.UploadID, expected.FileID)
		if errors.Is(err, domain.ErrUploadSessionNotFound) {
			return domainerr.Precondition("upload session changed before multipart resume reconciliation")
		}
		if err != nil {
			return err
		}
		current := uploadSessionFromStore(row)
		if !sameMultipartResumeSessionShape(current, expected) {
			return domainerr.Precondition("upload session shape changed before multipart resume reconciliation")
		}
		rows := make([]domain.UploadPart, len(parts))
		for i, p := range parts {
			rows[i] = domain.UploadPart(p)
		}
		if err := store.ReplaceUploadParts(ctx, expected.UploadID, rows); err != nil {
			return err
		}
		changed, err := store.RenewActiveUploadActivity(ctx, expected.UploadID, expected.FileID, true, time.Now())
		if err != nil {
			return err
		}
		if !changed {
			return domainerr.Precondition("upload session changed before multipart resume reconciliation")
		}
		return nil
	})
}

func sameMultipartResumeSessionShape(current, expected model.UploadSession) bool {
	return current.UploadID == expected.UploadID &&
		current.FileID == expected.FileID &&
		current.UploadType == expected.UploadType &&
		current.EntityID == expected.EntityID &&
		sameOptionalString(current.EntityType, expected.EntityType) &&
		current.FileName == expected.FileName &&
		current.FileSize == expected.FileSize &&
		sameOptionalInt64(current.FileLastModified, expected.FileLastModified) &&
		sameOptionalString(current.SlotID, expected.SlotID) &&
		sameOptionalString(current.AttemptID, expected.AttemptID) &&
		sameOptionalString(current.ExpectedFileID, expected.ExpectedFileID) &&
		current.RequestedMime == expected.RequestedMime &&
		current.TotalParts == expected.TotalParts &&
		current.ChunkSize == expected.ChunkSize
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

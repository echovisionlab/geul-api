package filemedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var (
	errMultipartUploadPartNotWritable = errors.New("upload session is no longer writable")
	errMultipartUploadedPartNotFound  = errors.New("multipart uploaded part not found")
)

type multipartUploadPartRequest struct {
	memberID      string
	uploadID      string
	correlationID string
	partNumber    int32
	session       model.UploadSession
	fileKey       string
}

type multipartPresignResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

type multipartConfirmResponse struct {
	ETag string `json:"etag"`
}

func isDirectS3UploadType(uploadType managev1.UploadType) bool {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO:
		return true
	default:
		return false
	}
}

func uploadSessionUsesDirectS3(session model.UploadSession) bool {
	value, ok := managev1.UploadType_value[session.UploadType]
	return ok && isDirectS3UploadType(managev1.UploadType(value))
}

func (s *FileService) rejectMultipartUploadPart(
	w http.ResponseWriter,
	r *http.Request,
	session model.UploadSession,
	reason string,
) bool {
	if err := s.abortUploadSession(r.Context(), session, reason); err != nil {
		if errors.Is(err, mediaasset.ErrUploadSessionNotAbortable) {
			http.Error(w, "Upload session is finalizing", http.StatusConflict)
			return false
		}
		slog.Warn("Failed to abort rejected multipart upload", "error", err, "uploadId", session.UploadID)
		http.Error(w, "Failed to abort rejected upload", http.StatusInternalServerError)
		return false
	}
	return true
}

func (s *FileService) recordUploadedPart(
	ctx context.Context,
	session model.UploadSession,
	part model.UploadPart,
) (bool, error) {
	recorded := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.UploadSession{}).
			Where("upload_id = ? AND file_id = ? AND status IN ?", session.UploadID, session.FileID, uploadPartWritableSessionStatuses()).
			Updates(structured.Fields{
				"status":           model.UploadSessionStatusUploading,
				"last_activity_at": part.UpdatedAt,
				"updated_at":       part.UpdatedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "upload_id"}, {Name: "part_number"}},
			DoUpdates: clause.AssignmentColumns([]string{"etag", "size", "updated_at"}),
		}).Create(&part).Error; err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return recorded, err
}

func (s *FileService) recordMultipartDetectedMIME(
	ctx context.Context,
	session model.UploadSession,
	detected string,
	now time.Time,
) error {
	result := s.db.WithContext(ctx).Model(&model.UploadSession{}).
		Where(
			"upload_id = ? AND file_id = ? AND status IN ? AND (detected_mime IS NULL OR detected_mime = '')",
			session.UploadID,
			session.FileID,
			uploadPartWritableSessionStatuses(),
		).
		Updates(structured.Fields{
			"detected_mime": detected,
			"verified_at":   now,
			"updated_at":    now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var current model.UploadSession
	if err := s.db.WithContext(ctx).
		Select("upload_id", "file_id", "status", "detected_mime").
		Where("upload_id = ? AND file_id = ?", session.UploadID, session.FileID).
		Take(&current).Error; err != nil {
		return err
	}
	if current.Status != model.UploadSessionStatusInitiated && current.Status != model.UploadSessionStatusUploading {
		return errMultipartUploadPartNotWritable
	}
	if current.DetectedMime == nil || *current.DetectedMime != detected {
		return fmt.Errorf("detected MIME type conflicts with concurrent upload")
	}
	return nil
}

func (s *FileService) claimMultipartUploadPartActivity(
	ctx context.Context,
	uploadID string,
	fileID string,
) (bool, error) {
	claimed := false
	err := withMultipartCompletionAdvisoryLock(
		ctx,
		s.db,
		uploadID,
		fileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			var err error
			claimed, err = lockedService.claimUploadPartActivity(ctx, uploadID, fileID, time.Now())
			return err
		},
	)
	return claimed, err
}

func (s *FileService) uploadAndRecordMultipartPart(
	ctx context.Context,
	session model.UploadSession,
	fileKey string,
	partNumber int32,
	partBody []byte,
	detectedMIME string,
) (string, error) {
	var etag string
	err := withMultipartCompletionAdvisoryLock(
		ctx,
		s.db,
		session.UploadID,
		session.FileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			now := time.Now()
			claimed, err := lockedService.claimUploadPartActivity(ctx, session.UploadID, session.FileID, now)
			if err != nil {
				return fmt.Errorf("claim multipart part activity: %w", err)
			}
			if !claimed {
				return errMultipartUploadPartNotWritable
			}
			if detectedMIME != "" {
				if err := lockedService.recordMultipartDetectedMIME(ctx, session, detectedMIME, now); err != nil {
					return fmt.Errorf("record multipart detected MIME: %w", err)
				}
			}

			if err := lockedService.db.WithContext(ctx).
				Where("upload_id = ? AND part_number = ?", session.UploadID, partNumber).
				Delete(&model.UploadPart{}).Error; err != nil {
				return fmt.Errorf("invalidate previous multipart uploaded part: %w", err)
			}

			output, err := lockedService.s3Client.UploadPart(ctx, &s3.UploadPartInput{
				Bucket:        aws.String(lockedService.s3Bucket),
				Key:           aws.String(fileKey),
				UploadId:      aws.String(session.UploadID),
				PartNumber:    aws.Int32(partNumber),
				Body:          bytes.NewReader(partBody),
				ContentLength: aws.Int64(int64(len(partBody))),
			})
			if err != nil {
				if updateErr := lockedService.recordRetryableMultipartPartFailure(ctx, session, time.Now()); updateErr != nil {
					slog.Warn(
						"Failed to preserve multipart upload session after retryable part failure",
						"error", updateErr,
						"uploadId", session.UploadID,
						"fileId", session.FileID,
					)
				}
				return fmt.Errorf("upload multipart part to storage: %w", err)
			}
			if output == nil || output.ETag == nil || strings.Trim(*output.ETag, "\"") == "" {
				return fmt.Errorf("upload multipart part to storage: response ETag is required")
			}

			etag = strings.Trim(*output.ETag, "\"")
			recorded, err := lockedService.recordUploadedPart(ctx, session, model.UploadPart{
				UploadID:   session.UploadID,
				PartNumber: partNumber,
				ETag:       etag,
				Size:       int64(len(partBody)),
				CreatedAt:  now,
				UpdatedAt:  now,
			})
			if err != nil {
				return fmt.Errorf("record multipart uploaded part: %w", err)
			}
			if !recorded {
				return errMultipartUploadPartNotWritable
			}
			return nil
		},
	)
	return etag, err
}

// HandleUploadPart relays bounded managed-public-asset parts through the
// authenticated API. Editor and large-media upload types must use direct S3.
func (s *FileService) HandleUploadPart(w http.ResponseWriter, r *http.Request) {
	request, ok := s.loadMultipartUploadPartRequest(w, r, http.MethodPut, true)
	if !ok || !s.validateMultipartUploadPartPermission(w, r, request) {
		return
	}
	if uploadSessionUsesDirectS3(request.session) {
		http.Error(w, "Upload type must use direct object storage", http.StatusBadRequest)
		return
	}

	progressEmitter := s.multipartPartProgressEmitter(r.Context(), request)
	chunkSize := multipartSessionChunkSize(request.session)
	expectedPartSize := expectedMultipartPartSize(request.session.FileSize, chunkSize, request.partNumber)
	if !s.validateRelayedMultipartUploadPart(w, r, request, progressEmitter, chunkSize, expectedPartSize) {
		return
	}

	var uploadBody io.Reader = r.Body
	detectedMIME := ""
	if request.partNumber == 1 {
		preparedBody, detected, prepared := s.prepareMultipartFirstPart(w, r, request, progressEmitter)
		if !prepared {
			return
		}
		uploadBody = preparedBody
		detectedMIME = detected
	}

	partBody, read := s.readMultipartPartBody(w, r, request, uploadBody)
	if !read {
		return
	}
	etag, uploaded := s.uploadRelayedMultipartPart(w, r, request, partBody, detectedMIME, progressEmitter)
	if !uploaded {
		return
	}
	s.publishMultipartPartProgress(r.Context(), request, progressEmitter)
	writeMultipartJSON(w, multipartConfirmResponse{ETag: etag})
}

func (s *FileService) validateRelayedMultipartUploadPart(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
	progressEmitter *fileIngestEventEmitter,
	chunkSize int64,
	expectedPartSize int64,
) bool {
	if request.partNumber != 1 && (request.session.DetectedMime == nil || *request.session.DetectedMime == "") {
		writeMultipartPartFailure(w, progressEmitter, "First part must be uploaded before other parts", http.StatusPreconditionFailed)
		return false
	}
	if r.ContentLength < 0 {
		writeMultipartPartFailure(w, progressEmitter, "Content-Length is required", http.StatusLengthRequired)
		return false
	}
	if r.ContentLength >= chunkSize+partUploadBodySlack {
		writeMultipartPartFailure(w, progressEmitter, "Chunk too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err := validateMultipartPartContentLength(request.session, request.partNumber, r.ContentLength); err != nil {
		writeMultipartPartFailure(w, progressEmitter, err.Error(), http.StatusBadRequest)
		return false
	}
	claimed, err := s.claimMultipartUploadPartActivity(r.Context(), request.session.UploadID, request.session.FileID)
	if err != nil {
		http.Error(w, "Failed to refresh upload session", http.StatusInternalServerError)
		return false
	}
	if !claimed {
		http.Error(w, "Upload session is no longer writable", http.StatusConflict)
		return false
	}
	return expectedPartSize > 0
}

func writeMultipartPartFailure(
	w http.ResponseWriter,
	progressEmitter *fileIngestEventEmitter,
	message string,
	status int,
) {
	if progressEmitter != nil {
		progressEmitter.publishFailed(message, 0, nil)
	}
	http.Error(w, message, status)
}

func (s *FileService) readMultipartPartBody(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
	body io.Reader,
) ([]byte, bool) {
	bodyBytes, err := readMultipartUploadBody(body, r.ContentLength)
	if err != nil {
		s.recordInterruptedMultipartUpload(
			r.Context(), request.session, request.uploadID, "Failed to read upload part body", err,
		)
		http.Error(w, "Upload interrupted", http.StatusRequestTimeout)
		return nil, false
	}
	return bodyBytes, true
}

func (s *FileService) uploadRelayedMultipartPart(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
	body []byte,
	detectedMIME string,
	progressEmitter *fileIngestEventEmitter,
) (string, bool) {
	etag, err := s.uploadAndRecordMultipartPart(
		r.Context(), request.session, request.fileKey, request.partNumber, body, detectedMIME,
	)
	if errors.Is(err, errMultipartUploadPartNotWritable) {
		http.Error(w, "Upload session is no longer writable", http.StatusConflict)
		return "", false
	}
	if err != nil {
		slog.Error(
			"Failed to upload relayed multipart part",
			"error", err,
			"fileKey", request.fileKey,
			"partNumber", request.partNumber,
		)
		writeMultipartPartFailure(w, progressEmitter, "Failed to upload part", http.StatusInternalServerError)
		return "", false
	}
	return etag, true
}

func (s *FileService) prepareMultipartFirstPart(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
	progressEmitter *fileIngestEventEmitter,
) (io.Reader, string, bool) {
	prefix, uploadBody, err := readMultipartSniffPrefix(r.Body, r.ContentLength)
	if err != nil {
		s.recordInterruptedMultipartUpload(
			r.Context(), request.session, request.uploadID, "Failed to read request body prefix", err,
		)
		http.Error(w, "Upload interrupted", http.StatusRequestTimeout)
		return nil, "", false
	}

	uploadType := managev1.UploadType(managev1.UploadType_value[request.session.UploadType])
	config := s.getUploadConfig(uploadType)
	if config == nil {
		writeMultipartPartFailure(w, progressEmitter, "Invalid upload type", http.StatusBadRequest)
		return nil, "", false
	}
	allowedSet := buildAllowedMimeSet(config.PermittedMimeTypes)
	detected := detectCanonicalMime(prefix, allowedSet)
	requestedCanonical := canonicalizeMimeType(request.session.RequestedMime, allowedSet)
	if _, allowed := allowedSet[detected]; !allowed {
		s.rejectMultipartFirstPart(w, r, request.session, "Detected MIME type not allowed")
		return nil, "", false
	}
	if err := validateMultipartFirstPartGLB(prefix, request.session, detected); err != nil {
		s.rejectMultipartFirstPart(w, r, request.session, err.Error())
		return nil, "", false
	}
	if requestedCanonical != "" && requestedCanonical != detected {
		s.rejectMultipartFirstPart(w, r, request.session, "MIME type mismatch")
		return nil, "", false
	}
	return uploadBody, detected, true
}

// HandleVerifyUploadPrefix validates only the bounded file prefix needed for
// MIME and container-shape verification. Multipart bytes never cross the API.
func (s *FileService) HandleVerifyUploadPrefix(w http.ResponseWriter, r *http.Request) {
	request, ok := s.loadMultipartUploadPartRequest(w, r, http.MethodPost, false)
	if !ok || !s.validateMultipartUploadPartPermission(w, r, request) {
		return
	}
	if !uploadSessionUsesDirectS3(request.session) {
		http.Error(w, "Upload type must use the authenticated API relay", http.StatusBadRequest)
		return
	}

	expectedPrefixSize := min(int64(multipartSniffBytes), request.session.FileSize)
	if r.ContentLength != expectedPrefixSize {
		http.Error(w, fmt.Sprintf("Upload prefix must contain exactly %d bytes", expectedPrefixSize), http.StatusBadRequest)
		return
	}
	prefix, err := io.ReadAll(http.MaxBytesReader(w, r.Body, expectedPrefixSize))
	if err != nil || int64(len(prefix)) != expectedPrefixSize {
		http.Error(w, "Failed to read upload prefix", http.StatusBadRequest)
		return
	}

	uploadType := managev1.UploadType(managev1.UploadType_value[request.session.UploadType])
	config := s.getUploadConfig(uploadType)
	if config == nil {
		http.Error(w, "Invalid upload type", http.StatusBadRequest)
		return
	}
	allowedSet := buildAllowedMimeSet(config.PermittedMimeTypes)
	detected := detectCanonicalMime(prefix, allowedSet)
	requestedCanonical := canonicalizeMimeType(request.session.RequestedMime, allowedSet)
	if _, allowed := allowedSet[detected]; !allowed {
		s.rejectMultipartFirstPart(w, r, request.session, "Detected MIME type not allowed")
		return
	}
	if err := validateMultipartFirstPartGLB(prefix, request.session, detected); err != nil {
		s.rejectMultipartFirstPart(w, r, request.session, err.Error())
		return
	}
	if requestedCanonical != "" && requestedCanonical != detected {
		s.rejectMultipartFirstPart(w, r, request.session, "MIME type mismatch")
		return
	}

	err = withMultipartCompletionAdvisoryLock(
		r.Context(), s.db, request.session.UploadID, request.session.FileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			now := time.Now()
			claimed, err := lockedService.claimUploadPartActivity(
				r.Context(), request.session.UploadID, request.session.FileID, now,
			)
			if err != nil {
				return err
			}
			if !claimed {
				return errMultipartUploadPartNotWritable
			}
			return lockedService.recordMultipartDetectedMIME(r.Context(), request.session, detected, now)
		},
	)
	if errors.Is(err, errMultipartUploadPartNotWritable) {
		http.Error(w, "Upload session is no longer writable", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "Failed to verify upload prefix", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandlePresignUploadPart returns one exact, short-lived S3 UploadPart URL.
func (s *FileService) HandlePresignUploadPart(w http.ResponseWriter, r *http.Request) {
	request, ok := s.loadMultipartUploadPartRequest(w, r, http.MethodPost, true)
	if !ok || !s.validateMultipartUploadPartPermission(w, r, request) {
		return
	}
	if !uploadSessionUsesDirectS3(request.session) {
		http.Error(w, "Upload type must use the authenticated API relay", http.StatusBadRequest)
		return
	}
	if request.session.DetectedMime == nil || strings.TrimSpace(*request.session.DetectedMime) == "" {
		http.Error(w, "Upload prefix must be verified before uploading parts", http.StatusPreconditionFailed)
		return
	}
	expectedSize := expectedMultipartPartSize(
		request.session.FileSize, multipartSessionChunkSize(request.session), request.partNumber,
	)
	if expectedSize <= 0 {
		http.Error(w, "Upload part is outside the expected multipart range", http.StatusBadRequest)
		return
	}

	err := withMultipartCompletionAdvisoryLock(
		r.Context(), s.db, request.session.UploadID, request.session.FileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			claimed, err := lockedService.claimUploadPartActivity(
				r.Context(), request.session.UploadID, request.session.FileID, time.Now(),
			)
			if err != nil {
				return err
			}
			if !claimed {
				return errMultipartUploadPartNotWritable
			}
			return lockedService.db.WithContext(r.Context()).
				Where("upload_id = ? AND part_number = ?", request.session.UploadID, request.partNumber).
				Delete(&model.UploadPart{}).Error
		},
	)
	if errors.Is(err, errMultipartUploadPartNotWritable) {
		http.Error(w, "Upload session is no longer writable", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "Failed to prepare upload part", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(multipartPresignTTL)
	presigned, err := s.s3PresignClient.PresignUploadPart(
		r.Context(),
		&s3.UploadPartInput{
			Bucket:        aws.String(s.s3Bucket),
			Key:           aws.String(request.fileKey),
			UploadId:      aws.String(request.session.UploadID),
			PartNumber:    aws.Int32(request.partNumber),
			ContentLength: aws.Int64(expectedSize),
		},
		func(options *s3.PresignOptions) { options.Expires = multipartPresignTTL },
	)
	if err != nil {
		http.Error(w, "Failed to sign upload part", http.StatusInternalServerError)
		return
	}
	writeMultipartJSON(w, multipartPresignResponse{
		URL: presigned.URL, ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// HandleConfirmUploadPart reconciles the browser result with MinIO actual and
// only then records the part as eligible for completion.
func (s *FileService) HandleConfirmUploadPart(w http.ResponseWriter, r *http.Request) {
	request, ok := s.loadMultipartUploadPartRequest(w, r, http.MethodPost, true)
	if !ok || !s.validateMultipartUploadPartPermission(w, r, request) {
		return
	}
	if !uploadSessionUsesDirectS3(request.session) {
		http.Error(w, "Upload type must use the authenticated API relay", http.StatusBadRequest)
		return
	}
	if request.session.DetectedMime == nil || strings.TrimSpace(*request.session.DetectedMime) == "" {
		http.Error(w, "Upload prefix must be verified before confirming parts", http.StatusPreconditionFailed)
		return
	}

	var confirmed model.UploadPart
	err := withMultipartCompletionAdvisoryLock(
		r.Context(), s.db, request.session.UploadID, request.session.FileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			now := time.Now()
			claimed, err := lockedService.claimUploadPartActivity(
				r.Context(), request.session.UploadID, request.session.FileID, now,
			)
			if err != nil {
				return err
			}
			if !claimed {
				return errMultipartUploadPartNotWritable
			}

			part, err := lockedService.loadExactMultipartPart(r.Context(), request)
			if err != nil {
				return err
			}
			part.CreatedAt = now
			part.UpdatedAt = now
			recorded, err := lockedService.recordUploadedPart(r.Context(), request.session, part)
			if err != nil {
				return err
			}
			if !recorded {
				return errMultipartUploadPartNotWritable
			}
			confirmed = part
			return nil
		},
	)
	if errors.Is(err, errMultipartUploadPartNotWritable) {
		http.Error(w, "Upload session is no longer writable", http.StatusConflict)
		return
	}
	if errors.Is(err, errMultipartUploadedPartNotFound) {
		http.Error(w, "Uploaded part was not found in object storage", http.StatusConflict)
		return
	}
	if err != nil {
		slog.Error("Failed to confirm multipart upload part", "error", err, "uploadId", request.uploadID, "partNumber", request.partNumber)
		http.Error(w, "Failed to confirm upload part", http.StatusInternalServerError)
		return
	}

	s.publishMultipartPartProgress(r.Context(), request, s.multipartPartProgressEmitter(r.Context(), request))
	writeMultipartJSON(w, multipartConfirmResponse{ETag: confirmed.ETag})
}

func (s *FileService) loadExactMultipartPart(
	ctx context.Context,
	request multipartUploadPartRequest,
) (model.UploadPart, error) {
	marker := strconv.Itoa(int(request.partNumber - 1))
	output, err := s.s3Client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:           aws.String(s.s3Bucket),
		Key:              aws.String(request.fileKey),
		UploadId:         aws.String(request.session.UploadID),
		PartNumberMarker: aws.String(marker),
		MaxParts:         aws.Int32(1),
	})
	if err != nil {
		return model.UploadPart{}, fmt.Errorf("list multipart upload part: %w", err)
	}
	if output == nil || len(output.Parts) != 1 || aws.ToInt32(output.Parts[0].PartNumber) != request.partNumber {
		return model.UploadPart{}, errMultipartUploadedPartNotFound
	}
	part := output.Parts[0]
	etag := strings.Trim(aws.ToString(part.ETag), "\"")
	expectedSize := expectedMultipartPartSize(
		request.session.FileSize, multipartSessionChunkSize(request.session), request.partNumber,
	)
	if etag == "" || aws.ToInt64(part.Size) != expectedSize {
		return model.UploadPart{}, fmt.Errorf("object-store part identity does not match the upload session")
	}
	return model.UploadPart{
		UploadID: request.session.UploadID, PartNumber: request.partNumber, ETag: etag, Size: expectedSize,
	}, nil
}

func (s *FileService) loadMultipartUploadPartRequest(
	w http.ResponseWriter,
	r *http.Request,
	expectedMethod string,
	requirePartNumber bool,
) (multipartUploadPartRequest, bool) {
	var request multipartUploadPartRequest
	if r.Method != expectedMethod {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return request, false
	}
	gatewayIdentity, ok := auth.GatewayIdentityFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return request, false
	}
	fileID := r.URL.Query().Get("fileId")
	request.uploadID = r.URL.Query().Get("uploadId")
	request.correlationID = r.URL.Query().Get("correlationId")
	request.memberID = gatewayIdentity.MemberID.String()
	if len(fileID) > 64 || len(request.uploadID) > 256 || len(request.correlationID) > 128 {
		http.Error(w, "Invalid parameter length", http.StatusBadRequest)
		return request, false
	}
	if fileID == "" || request.uploadID == "" {
		http.Error(w, "Missing required parameters: fileId, uploadId", http.StatusBadRequest)
		return request, false
	}
	if requirePartNumber {
		partNumber := r.URL.Query().Get("partNumber")
		parsedPartNumber, err := strconv.ParseInt(partNumber, 10, 32)
		if err != nil || parsedPartNumber < 1 || parsedPartNumber > 10000 {
			http.Error(w, "Invalid partNumber (must be 1-10000)", http.StatusBadRequest)
			return request, false
		}
		request.partNumber = int32(parsedPartNumber)
	}
	if err := s.db.WithContext(r.Context()).
		Where("upload_id = ? AND file_id = ?", request.uploadID, fileID).
		First(&request.session).Error; err != nil {
		status := http.StatusInternalServerError
		message := "Failed to load upload session"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
			message = "Upload session not found"
		}
		http.Error(w, message, status)
		return request, false
	}
	fileKey, err := uploadSessionObjectKey(request.session)
	if err != nil {
		http.Error(w, "Invalid upload session target", http.StatusInternalServerError)
		return request, false
	}
	request.fileKey = fileKey
	return request, true
}

func (s *FileService) validateMultipartUploadPartPermission(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
) bool {
	if err := s.checkPartUploadPermission(r.Context(), request.memberID, request.session); err != nil {
		slog.Warn(
			"Upload part permission denied",
			"member_id", request.memberID,
			"entityType", derefString(request.session.EntityType),
			"entityID", request.session.EntityID,
			"uploadType", request.session.UploadType,
		)
		http.Error(w, "permission denied", http.StatusForbidden)
		return false
	}
	return true
}

func (s *FileService) multipartPartProgressEmitter(
	ctx context.Context,
	request multipartUploadPartRequest,
) *fileIngestEventEmitter {
	emitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(request.session.EntityType),
		request.session.EntityID,
		request.correlationID,
		request.session.FileID,
		request.session.FileSize,
	)
	if emitter != nil {
		if err := s.bindUploadSessionIngestEmitter(emitter, request.session); err != nil {
			return nil
		}
	}
	return emitter
}

func (s *FileService) publishMultipartPartProgress(
	ctx context.Context,
	request multipartUploadPartRequest,
	progressEmitter *fileIngestEventEmitter,
) {
	if progressEmitter == nil {
		return
	}
	var uploadedBytes int64
	if err := s.db.WithContext(ctx).Model(&model.UploadPart{}).
		Where("upload_id = ?", request.session.UploadID).
		Select("COALESCE(SUM(size), 0)").Scan(&uploadedBytes).Error; err != nil {
		return
	}
	progress := int32(0)
	if request.session.FileSize > 0 {
		progress = min(int32((uploadedBytes*100)/request.session.FileSize), 100)
	}
	progressEmitter.publishUploading(progress, &uploadedBytes)
}

func validateMultipartFirstPartGLB(prefix []byte, session model.UploadSession, detected string) error {
	if detected != "model/gltf-binary" {
		return nil
	}
	return validateGLBUploadSize(prefix, session.FileSize)
}

func (s *FileService) rejectMultipartFirstPart(
	w http.ResponseWriter,
	r *http.Request,
	session model.UploadSession,
	reason string,
) {
	if s.rejectMultipartUploadPart(w, r, session, reason) {
		http.Error(w, reason, http.StatusBadRequest)
	}
}

func writeMultipartJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("Failed to write multipart control response", "error", err)
	}
}

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

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
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

// failureAttrs names the upload part for the HTTP request record.
func (request multipartUploadPartRequest) failureAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("upload_id", request.uploadID),
		slog.String("file_id", request.session.FileID),
		slog.Int("part_number", int(request.partNumber)),
	}
}

// recordUploadFailure gives err to the HTTP request record, which logs the
// failed request once with the status the handler writes.
func recordUploadFailure(r *http.Request, err error, attrs ...slog.Attr) {
	failurecause.RecorderFrom(r.Context()).Record(failurecause.Of(failurecause.WithAttrs(err, attrs...)))
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

func (s *Service) rejectMultipartUploadPart(
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
		recordUploadFailure(r, fmt.Errorf("abort rejected multipart upload: %w", err), slog.String("upload_id", session.UploadID))
		http.Error(w, "Failed to abort rejected upload", http.StatusInternalServerError)
		return false
	}
	return true
}

func (s *Service) recordUploadedPart(
	ctx context.Context,
	session model.UploadSession,
	part model.UploadPart,
) (bool, error) {
	recorded := false
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		changed, err := store.MarkUploadPartUploading(ctx, session.UploadID, session.FileID, part.UpdatedAt)
		if err != nil || !changed {
			return err
		}
		if err := store.UpsertUploadPart(ctx, domain.UploadPart(part)); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return recorded, err
}

func (s *Service) recordMultipartDetectedMIME(
	ctx context.Context,
	session model.UploadSession,
	detected string,
	now time.Time,
) error {
	changed := false
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		changed, err = unit.Store().DetectUploadMIME(ctx, session.UploadID, session.FileID, detected, now)
		return err
	}); err != nil {
		return err
	}
	if changed {
		return nil
	}
	var current domain.UploadMIMEState
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		current, err = store.UploadMIMEState(ctx, session.UploadID, session.FileID)
		return err
	}); err != nil {
		return err
	}
	if current.Status != domain.UploadSessionStatusInitiated && current.Status != domain.UploadSessionStatusUploading {
		return errMultipartUploadPartNotWritable
	}
	if current.DetectedMime == nil || *current.DetectedMime != detected {
		return fmt.Errorf("detected MIME type conflicts with concurrent upload")
	}
	return nil
}

func (s *Service) claimMultipartUploadPartActivity(
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

func (s *Service) uploadAndRecordMultipartPart(
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

			if err := lockedService.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
				return unit.Store().DeleteUploadPart(ctx, session.UploadID, partNumber)
			}); err != nil {
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
					slog.ErrorContext(ctx, "Failed to preserve multipart upload session after retryable part failure",
						failurecause.Attr(updateErr),
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
func (s *Service) HandleUploadPart(w http.ResponseWriter, r *http.Request) {
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

func (s *Service) validateRelayedMultipartUploadPart(
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
		writeMultipartPartFailure(w, progressEmitter, clientErrorText(err), http.StatusBadRequest)
		return false
	}
	claimed, err := s.claimMultipartUploadPartActivity(r.Context(), request.session.UploadID, request.session.FileID)
	if err != nil {
		recordUploadFailure(r, fmt.Errorf("refresh upload session: %w", err), request.failureAttrs()...)
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

func (s *Service) readMultipartPartBody(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
	body io.Reader,
) ([]byte, bool) {
	bodyBytes, err := readMultipartUploadBody(body, r.ContentLength)
	if err != nil {
		s.recordInterruptedMultipartUpload(
			r.Context(), request.session, request.uploadID, "read upload part body", err,
		)
		http.Error(w, "Upload interrupted", http.StatusRequestTimeout)
		return nil, false
	}
	return bodyBytes, true
}

func (s *Service) uploadRelayedMultipartPart(
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
		recordUploadFailure(r, fmt.Errorf("upload relayed multipart part: %w", err), request.failureAttrs()...)
		writeMultipartPartFailure(w, progressEmitter, "Failed to upload part", http.StatusInternalServerError)
		return "", false
	}
	return etag, true
}

func (s *Service) prepareMultipartFirstPart(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
	progressEmitter *fileIngestEventEmitter,
) (io.Reader, string, bool) {
	prefix, uploadBody, err := readMultipartSniffPrefix(r.Body, r.ContentLength)
	if err != nil {
		s.recordInterruptedMultipartUpload(
			r.Context(), request.session, request.uploadID, "read request body prefix", err,
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
		s.rejectMultipartFirstPart(w, r, request.session, clientErrorText(err))
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
func (s *Service) HandleVerifyUploadPrefix(w http.ResponseWriter, r *http.Request) {
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
		s.rejectMultipartFirstPart(w, r, request.session, clientErrorText(err))
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
		recordUploadFailure(r, fmt.Errorf("verify upload prefix: %w", err), request.failureAttrs()...)
		http.Error(w, "Failed to verify upload prefix", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandlePresignUploadPart returns one exact, short-lived S3 UploadPart URL.
func (s *Service) HandlePresignUploadPart(w http.ResponseWriter, r *http.Request) {
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
			return lockedService.units.Transaction(r.Context(), false, func(unit mutation.Tx[Store]) error {
				return unit.Store().DeleteUploadPart(r.Context(), request.session.UploadID, request.partNumber)
			})
		},
	)
	if errors.Is(err, errMultipartUploadPartNotWritable) {
		http.Error(w, "Upload session is no longer writable", http.StatusConflict)
		return
	}
	if err != nil {
		recordUploadFailure(r, fmt.Errorf("prepare upload part: %w", err), request.failureAttrs()...)
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
		recordUploadFailure(r, fmt.Errorf("sign upload part: %w", err), request.failureAttrs()...)
		http.Error(w, "Failed to sign upload part", http.StatusInternalServerError)
		return
	}
	writeMultipartJSON(w, multipartPresignResponse{
		URL: presigned.URL, ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// HandleConfirmUploadPart reconciles the browser result with MinIO actual and
// only then records the part as eligible for completion.
func (s *Service) HandleConfirmUploadPart(w http.ResponseWriter, r *http.Request) {
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
		recordUploadFailure(r, fmt.Errorf("confirm multipart upload part: %w", err), request.failureAttrs()...)
		http.Error(w, "Failed to confirm upload part", http.StatusInternalServerError)
		return
	}

	s.publishMultipartPartProgress(r.Context(), request, s.multipartPartProgressEmitter(r.Context(), request))
	writeMultipartJSON(w, multipartConfirmResponse{ETag: confirmed.ETag})
}

func (s *Service) loadExactMultipartPart(
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

func (s *Service) loadMultipartUploadPartRequest(
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
		http.Error(w, "authentication required: sign in through the site login page and send the session cookie", http.StatusUnauthorized)
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
	if _, err := uuidutil.ParseCanonical(fileID, "fileId"); err != nil {
		http.Error(w, "Invalid fileId (must be a canonical UUID)", http.StatusBadRequest)
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
	var row domain.UploadSession
	if err := s.units.Read(r.Context(), func(store Store) error {
		var err error
		row, err = store.UploadSession(r.Context(), request.uploadID, fileID, false)
		return err
	}); err != nil {
		status := http.StatusInternalServerError
		message := "Failed to load upload session"
		if errors.Is(err, domain.ErrUploadSessionNotFound) {
			status = http.StatusNotFound
			message = "Upload session not found"
		}
		if status != http.StatusNotFound {
			recordUploadFailure(r, fmt.Errorf("load upload session: %w", err), slog.String("upload_id", request.uploadID))
		}
		http.Error(w, message, status)
		return request, false
	}
	request.session = uploadSessionFromStore(row)
	fileKey, err := uploadSessionObjectKey(request.session)
	if err != nil {
		recordUploadFailure(r, fmt.Errorf("resolve upload session target: %w", err), request.failureAttrs()...)
		http.Error(w, "Invalid upload session target", http.StatusInternalServerError)
		return request, false
	}
	request.fileKey = fileKey
	return request, true
}

func (s *Service) validateMultipartUploadPartPermission(
	w http.ResponseWriter,
	r *http.Request,
	request multipartUploadPartRequest,
) bool {
	if err := s.checkPartUploadPermission(r.Context(), request.memberID, request.session); err != nil {
		recordUploadFailure(r, err, append(request.failureAttrs(),
			slog.String("entity_type", derefString(request.session.EntityType)),
			slog.String("entity_id", request.session.EntityID),
			slog.String("upload_type", request.session.UploadType),
		)...)
		http.Error(w, "permission denied", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Service) multipartPartProgressEmitter(
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
			slog.ErrorContext(ctx, "Failed to restore multipart progress emitter", failurecause.Attr(err), "uploadId", request.uploadID)
			return nil
		}
	}
	return emitter
}

func (s *Service) publishMultipartPartProgress(
	ctx context.Context,
	request multipartUploadPartRequest,
	progressEmitter *fileIngestEventEmitter,
) {
	if progressEmitter == nil {
		return
	}
	var uploadedBytes int64
	if err := s.readUploadFacts(ctx, func(store Store) error {
		var err error
		uploadedBytes, err = store.UploadedPartBytes(ctx, request.session.UploadID)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Failed to load multipart progress", failurecause.Attr(err), "uploadId", request.uploadID)
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

func (s *Service) rejectMultipartFirstPart(
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
		slog.Warn("Failed to write multipart control response", failurecause.Attr(err))
	}
}

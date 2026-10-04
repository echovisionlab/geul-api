package filemedia

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) PrepareClientMediaUpload(ctx context.Context, req *connect.Request[managev1.PrepareClientMediaUploadRequest]) (*connect.Response[managev1.PrepareClientMediaUploadResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	r := req.Msg
	var bundleID string
	err := withMultipartCompletionAdvisoryLock(ctx, s.db, r.GetUploadId(), r.GetFileId(), func(db *gorm.DB) error {
		svc := *s
		svc.db = db
		session, err := svc.authorizedClientMediaSession(ctx, r.GetUploadId(), r.GetFileId())
		if err != nil {
			return err
		}
		if !clientMediaSessionWritable(session) {
			return errs.FailedPrecondition("upload session is not writable")
		}
		bundleID, err = svc.prepareMediaBundle(ctx, session, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.PrepareClientMediaUploadResponse{BundleId: bundleID}), nil
}
func clientMediaPlanFromRequest(request *managev1.PrepareClientMediaUploadRequest, memberID string) (clientMediaPlan, error) {
	plan := clientMediaPlan{Version: clientMediaPlanVersion, DurationSeconds: request.GetDurationSeconds(), MemberID: memberID}
	switch request.GetKind() {
	case managev1.ClientMediaKind_CLIENT_MEDIA_KIND_AUDIO:
		plan.Kind = "audio"
	case managev1.ClientMediaKind_CLIENT_MEDIA_KIND_VIDEO:
		plan.Kind = "video"
	default:
		return plan, errs.InvalidArgument("kind", "unsupported media kind")
	}
	for _, artifact := range request.GetArtifacts() {
		plan.Artifacts = append(plan.Artifacts, clientMediaArtifact{Path: artifact.GetPath(), MimeType: normalizeMimeType(artifact.GetMimeType()), Size: artifact.GetSize(), SHA256: artifact.GetSha256(), DerivativeType: int32(artifact.GetDerivativeType())})
	}
	return plan, nil
}

func (s *FileService) prepareMediaBundle(ctx context.Context, session model.UploadSession, request *managev1.PrepareClientMediaUploadRequest) (string, error) {
	plan, err := clientMediaPlanFromRequest(request, auth.GetUser(ctx).MemberID.String())
	if err != nil {
		return "", err
	}
	if err := validateClientMediaPlan(plan, session); err != nil {
		return "", errs.InvalidArgument("artifacts", err.Error())
	}
	manifest, err := encodeClientMediaPlan(plan)
	if err != nil {
		return "", errs.Internal(err)
	}
	if session.ClientMediaBundleID != nil {
		existing, err := decodeClientMediaPlan(session)
		if err != nil {
			return "", errs.Internal(err)
		}
		prior, err := encodeClientMediaPlan(existing)
		if err != nil {
			return "", errs.Internal(err)
		}
		if prior != manifest {
			return "", errs.FailedPrecondition("client media plan conflicts with existing bundle")
		}
		return *session.ClientMediaBundleID, nil
	}
	bundleID := uuid.NewString()
	result := s.db.WithContext(ctx).Model(&model.UploadSession{}).
		Where("upload_id = ? AND file_id = ? AND status IN ? AND client_media_bundle_id IS NULL", session.UploadID, session.FileID, []model.UploadSessionStatus{model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading}).
		Updates(structured.Fields{"client_media_bundle_id": bundleID, "client_media_manifest": manifest, "last_activity_at": time.Now()})
	if result.Error != nil {
		return "", errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return "", errs.FailedPrecondition("upload session changed before bundle preparation")
	}
	return bundleID, nil
}

func clientMediaSessionWritable(session model.UploadSession) bool {
	return session.Status == model.UploadSessionStatusInitiated || session.Status == model.UploadSessionStatusUploading
}
func (s *FileService) authorizedClientMediaSession(ctx context.Context, uploadID, fileID string) (model.UploadSession, error) {
	var session model.UploadSession
	if !IsValidUUID(fileID) || uploadID == "" || len(uploadID) > 256 {
		return session, errs.InvalidArgument("upload_id", "invalid upload identity")
	}
	if err := s.db.WithContext(ctx).Where("upload_id = ? AND file_id = ?", uploadID, fileID).Take(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return session, errs.NotFoundMsg("upload session not found")
		}
		return session, errs.Internal(err)
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return session, errs.AuthenticationRequired()
	}
	return session, s.checkPartUploadPermission(ctx, user.MemberID.String(), session)
}

// HandleClientMediaArtifact relays only a hash-verified artifact from the sealed plan.
func (s *FileService) HandleClientMediaArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", 405)
		return
	}
	identity, ok := auth.GatewayIdentityFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", 401)
		return
	}
	fileID, uploadID, bundleID, path := r.URL.Query().Get("fileId"), r.URL.Query().Get("uploadId"), r.URL.Query().Get("bundleId"), r.URL.Query().Get("path")
	if !IsValidUUID(fileID) || !IsValidUUID(bundleID) || uploadID == "" || len(uploadID) > 256 || !clientMediaPathPattern.MatchString(path) {
		http.Error(w, "invalid artifact identity", 400)
		return
	}
	// The same lock seals PUT and completion; no upload can race canonical registration.
	err := withMultipartCompletionAdvisoryLock(r.Context(), s.db, uploadID, fileID, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			svc := *s
			svc.db = tx
			session, artifact, err := svc.authorizeMediaArtifact(r.Context(), identity.MemberID.String(), fileID, uploadID, bundleID, path)
			if err != nil {
				return err
			}
			if err := svc.stageMediaArtifact(r, bundleID, *artifact); err != nil {
				return err
			}
			svc.refreshUploadSessionActivity(r.Context(), session.UploadID)
			return nil
		})
	})
	if err != nil {
		status := clientArtifactHTTPStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *FileService) authorizeMediaArtifact(ctx context.Context, memberID, fileID, uploadID, bundleID, path string) (model.UploadSession, *clientMediaArtifact, error) {
	var session model.UploadSession
	if err := s.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("upload_id = ? AND file_id = ?", uploadID, fileID).Take(&session).Error; err != nil {
		return session, nil, errs.NotFoundMsg("upload session not found")
	}
	if err := s.checkPartUploadPermission(ctx, memberID, session); err != nil {
		return session, nil, errs.PermissionDenied("artifact upload permission denied")
	}
	writable, err := s.clientMediaArtifactWritable(ctx, session)
	if err != nil {
		return session, nil, err
	}
	if !writable || session.ClientMediaBundleID == nil || *session.ClientMediaBundleID != bundleID {
		return session, nil, errs.FailedPrecondition("artifact bundle is not writable")
	}
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return session, nil, errs.Internal(err)
	}
	if plan.MemberID != memberID {
		return session, nil, errs.PermissionDenied("client media bundle belongs to another member")
	}
	artifact := plan.artifact(path)
	if artifact == nil {
		return session, nil, errs.InvalidArgument("path", "artifact is absent from plan")
	}
	return session, artifact, nil
}

func (s *FileService) stageMediaArtifact(request *http.Request, bundleID string, artifact clientMediaArtifact) error {
	if request.ContentLength != artifact.Size || normalizeMimeType(request.Header.Get("Content-Type")) != artifact.MimeType {
		return errs.InvalidArgument("body", "artifact size or MIME differs from plan")
	}
	tmp, err := os.CreateTemp("", "dsub-client-artifact-*")
	if err != nil {
		return errs.Internal(err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	size, digest, err := hashArtifactBody(request.Body, tmp, artifact.Size)
	if err != nil || size != artifact.Size || digest != artifact.SHA256 {
		return errs.InvalidArgument("body", "artifact size or SHA256 mismatch")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return errs.Internal(err)
	}
	_, err = s.s3Client.PutObject(request.Context(), &s3.PutObjectInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(clientMediaStagingKey(bundleID, artifact.Path)), Body: tmp,
		ContentLength: aws.Int64(size), ContentType: aws.String(artifact.MimeType),
	})
	if err != nil {
		return errs.Internal(fmt.Errorf("stage client artifact: %w", err))
	}
	return nil
}

func clientArtifactHTTPStatus(err error) int {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeFailedPrecondition:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func clientMediaBundleMatches(session model.UploadSession, bundleID string) error {
	if session.ClientMediaBundleID == nil {
		if bundleID != "" {
			return errs.FailedPrecondition("client media bundle is not prepared")
		}
		return nil
	}
	if strings.TrimSpace(bundleID) != *session.ClientMediaBundleID {
		return errs.FailedPrecondition("completion requires the prepared client media bundle")
	}
	return nil
}

// Finalizing repair changes staging only, and remains serialized with completion.
func (s *FileService) clientMediaArtifactWritable(ctx context.Context, session model.UploadSession) (bool, error) {
	if clientMediaSessionWritable(session) {
		return true, nil
	}
	if session.Status != model.UploadSessionStatusFinalizing {
		return false, nil
	}
	var file model.File
	err := s.db.WithContext(ctx).Where("id = ?", session.FileID).Take(&file).Error
	if err == gorm.ErrRecordNotFound {
		return true, nil
	}
	if err != nil {
		return false, errs.Internal(err)
	}
	// The durable receipt seals uploads even if a later deletion/corruption changes derivatives.
	return file.ClientMediaBundleID == nil && file.DeleteRequestedAt == nil, nil
}

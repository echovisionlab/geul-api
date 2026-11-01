package filemedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// PrepareClientMediaUploadCommand registers the browser-built media bundle
// of an upload before its completion.
type PrepareClientMediaUploadCommand struct {
	FileID          string
	UploadID        string
	Kind            managev1.ClientMediaKind
	DurationSeconds float64
	Artifacts       []ClientMediaUploadArtifact
}

// ClientMediaUploadArtifact is one object of a client media bundle.
type ClientMediaUploadArtifact struct {
	Path           string
	MimeType       string
	Size           int64
	SHA256         string
	DerivativeType managev1.FileDerivativeType
}

// PrepareClientMediaUpload returns the ID of the bundle it registered.
func (s *Service) PrepareClientMediaUpload(ctx context.Context, command PrepareClientMediaUploadCommand) (string, error) {
	var bundleID string
	err := s.ingestLocks.Multipart(ctx, command.UploadID, command.FileID, func(scope domain.IngestSession) error {
		lockedService, err := s.ingestService(scope)
		if err != nil {
			return err
		}
		session, err := lockedService.authorizedClientMediaSession(ctx, command.UploadID, command.FileID)
		if err != nil {
			return err
		}
		if !clientMediaSessionWritable(session) {
			return domainerr.Precondition("upload session is not writable")
		}
		bundleID, err = lockedService.prepareMediaBundle(ctx, session, command)
		return err
	})
	if err != nil {
		return "", err
	}
	return bundleID, nil
}

func clientMediaPlanFromCommand(command PrepareClientMediaUploadCommand, memberID string) (clientMediaPlan, error) {
	plan := clientMediaPlan{Version: clientMediaPlanVersion, DurationSeconds: command.DurationSeconds, MemberID: memberID}
	switch command.Kind {
	case managev1.ClientMediaKind_CLIENT_MEDIA_KIND_AUDIO:
		plan.Kind = "audio"
	case managev1.ClientMediaKind_CLIENT_MEDIA_KIND_VIDEO:
		plan.Kind = "video"
	default:
		return plan, domainerr.InvalidField("kind", "unsupported media kind")
	}
	for _, artifact := range command.Artifacts {
		plan.Artifacts = append(plan.Artifacts, clientMediaArtifact{Path: artifact.Path, MimeType: normalizeMimeType(artifact.MimeType), Size: artifact.Size, SHA256: artifact.SHA256, DerivativeType: int32(artifact.DerivativeType)})
	}
	return plan, nil
}

func (s *Service) prepareMediaBundle(ctx context.Context, session domain.UploadSession, command PrepareClientMediaUploadCommand) (string, error) {
	plan, err := clientMediaPlanFromCommand(command, auth.GetUser(ctx).MemberID.String())
	if err != nil {
		return "", err
	}
	if err := validateClientMediaPlan(plan, session); err != nil {
		return "", domainerr.InvalidField("artifacts", err.Error())
	}
	manifest, err := encodeClientMediaPlan(plan)
	if err != nil {
		return "", err
	}
	if session.ClientMediaBundleID != nil {
		existing, err := decodeClientMediaPlan(session)
		if err != nil {
			return "", err
		}
		prior, err := encodeClientMediaPlan(existing)
		if err != nil {
			return "", err
		}
		if prior != manifest {
			return "", domainerr.Precondition("client media plan conflicts with existing bundle")
		}
		return *session.ClientMediaBundleID, nil
	}
	bundleID := uuid.NewString()
	changed := false
	err = s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		changed, err = unit.Store().PrepareUploadBundle(ctx, session.UploadID, session.FileID, bundleID, manifest, time.Now())
		return err
	})
	if err != nil {
		return "", err
	}
	if !changed {
		return "", domainerr.Precondition("upload session changed before bundle preparation")
	}

	return bundleID, nil
}

func clientMediaSessionWritable(session domain.UploadSession) bool {
	return session.Status == domain.UploadSessionStatusInitiated || session.Status == domain.UploadSessionStatusUploading
}
func (s *Service) authorizedClientMediaSession(ctx context.Context, uploadID, fileID string) (domain.UploadSession, error) {
	var session domain.UploadSession
	if !IsValidUUID(fileID) || uploadID == "" || len(uploadID) > 256 {
		return session, domainerr.InvalidField("upload_id", "invalid upload identity")
	}
	var row domain.UploadSession
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		row, err = store.UploadSession(ctx, uploadID, fileID, false)
		return err
	}); err != nil {
		if errors.Is(err, domain.ErrUploadSessionNotFound) {
			return session, domainerr.New(domainerr.NotFound, "", "upload session not found")
		}
		return session, err
	}
	session = row

	user := auth.GetUser(ctx)
	if user == nil {
		return session, domainerr.AuthenticationRequired()
	}
	return session, s.checkPartUploadPermission(ctx, user.MemberID.String(), session)
}

// HandleClientMediaArtifact relays only a hash-verified artifact from the sealed plan.
func (s *Service) HandleClientMediaArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, ok := auth.GatewayIdentityFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	fileID, uploadID, bundleID, path := r.URL.Query().Get("fileId"), r.URL.Query().Get("uploadId"), r.URL.Query().Get("bundleId"), r.URL.Query().Get("path")
	if !IsValidUUID(fileID) || !IsValidUUID(bundleID) || uploadID == "" || len(uploadID) > 256 || !clientMediaPathPattern.MatchString(path) {
		http.Error(w, "invalid artifact identity", 400)
		return
	}
	// The same lock seals PUT and completion; no upload can race canonical registration.
	err := s.ingestLocks.Multipart(r.Context(), uploadID, fileID, func(scope domain.IngestSession) error {
		lockedService, err := s.ingestService(scope)
		if err != nil {
			return err
		}
		return lockedService.units.Transaction(r.Context(), false, func(unit mutation.Tx[Store]) error {
			svc := *lockedService
			svc.uploadReads = boundFileStoreReader{unit.Store()}
			svc.ownerPreflights = boundFileStoreReader{unit.Store()}
			session, artifact, err := svc.authorizeMediaArtifact(r.Context(), identity.MemberID.String(), fileID, uploadID, bundleID, path)
			if err != nil {
				return err
			}
			if err := svc.stageMediaArtifact(r, bundleID, *artifact); err != nil {
				return err
			}
			if err := unit.Store().RenewUploadActivity(r.Context(), session.UploadID, time.Now()); err != nil {
				slog.ErrorContext(r.Context(), "Failed to refresh upload session activity", failurecause.Attr(err), "uploadId", session.UploadID)
			}
			return nil
		})
	})
	if err != nil {
		status := clientArtifactHTTPStatus(err)
		failurecause.RecorderFrom(r.Context()).Record(failurecause.Of(err))
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) authorizeMediaArtifact(ctx context.Context, memberID, fileID, uploadID, bundleID, path string) (domain.UploadSession, *clientMediaArtifact, error) {
	var session domain.UploadSession
	var row domain.UploadSession
	if err := s.readUploadFacts(ctx, func(store Store) error {
		var err error
		row, err = store.UploadSession(ctx, uploadID, fileID, true)
		return err
	}); err != nil {
		return session, nil, domainerr.New(domainerr.NotFound, "", "upload session not found").WithCause(failurecause.WithAttrs(err, slog.String("upload_id", uploadID), slog.String("file_id", fileID)))
	}
	session = row

	if err := s.checkPartUploadPermission(ctx, memberID, session); err != nil {
		return session, nil, domainerr.Denied("artifact upload permission denied").WithCause(failurecause.WithAttrs(err, slog.String("upload_id", uploadID), slog.String("file_id", fileID)))
	}
	writable, err := s.clientMediaArtifactWritable(ctx, session)
	if err != nil {
		return session, nil, err
	}
	if !writable || session.ClientMediaBundleID == nil || *session.ClientMediaBundleID != bundleID {
		return session, nil, domainerr.Precondition("artifact bundle is not writable")
	}
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return session, nil, err
	}
	if plan.MemberID != memberID {
		return session, nil, domainerr.Denied("client media bundle belongs to another member")
	}
	artifact := plan.artifact(path)
	if artifact == nil {
		return session, nil, domainerr.InvalidField("path", "artifact is absent from plan")
	}
	return session, artifact, nil
}

func (s *Service) stageMediaArtifact(request *http.Request, bundleID string, artifact clientMediaArtifact) error {
	if request.ContentLength != artifact.Size || normalizeMimeType(request.Header.Get("Content-Type")) != artifact.MimeType {
		return domainerr.InvalidField("body", "artifact size or MIME differs from plan")
	}
	tmp, err := os.CreateTemp("", "dsub-client-artifact-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() {
		if err := tmp.Close(); err != nil {
			slog.ErrorContext(request.Context(), "Failed to close staged media artifact", failurecause.Attr(err), "bundle_id", bundleID)
		}
	}()
	size, digest, err := hashArtifactBody(request.Body, tmp, artifact.Size)
	if err != nil || size != artifact.Size || digest != artifact.SHA256 {
		return domainerr.InvalidField("body", "artifact size or SHA256 mismatch").WithCause(failurecause.WithAttrs(err, slog.String("bundle_id", bundleID)))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = s.s3Client.PutObject(request.Context(), &s3.PutObjectInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(clientMediaStagingKey(bundleID, artifact.Path)), Body: tmp,
		ContentLength: aws.Int64(size), ContentType: aws.String(artifact.MimeType),
	})
	if err != nil {
		return fmt.Errorf("stage client artifact: %w", err)
	}
	return nil
}

func clientMediaBundleMatches(session domain.UploadSession, bundleID string) error {
	if session.ClientMediaBundleID == nil {
		if bundleID != "" {
			return domainerr.Precondition("client media bundle is not prepared")
		}
		return nil
	}
	if strings.TrimSpace(bundleID) != *session.ClientMediaBundleID {
		return domainerr.Precondition("completion requires the prepared client media bundle")
	}
	return nil
}

// Finalizing repair changes staging only, and remains serialized with completion.
func (s *Service) clientMediaArtifactWritable(ctx context.Context, session domain.UploadSession) (bool, error) {
	if clientMediaSessionWritable(session) {
		return true, nil
	}
	if session.Status != domain.UploadSessionStatusFinalizing {
		return false, nil
	}
	var file File
	err := s.readUploadFacts(ctx, func(store Store) error {
		var err error
		file, err = store.IngestFile(ctx, session.FileID)
		return err
	})
	if errors.Is(err, domain.ErrFileNotFound) {
		return true, nil
	}

	if err != nil {
		return false, err
	}
	// The durable receipt seals uploads even if a later deletion/corruption changes derivatives.
	return file.ClientMediaBundleID == nil && file.DeleteRequestedAt == nil, nil
}

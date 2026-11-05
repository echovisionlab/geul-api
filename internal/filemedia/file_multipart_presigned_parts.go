package filemedia

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

// maxPresignedUploadParts bounds one presigning call. Each URL expires after
// multipartPresignTTL, so a larger File asks again for its remaining parts.
const maxPresignedUploadParts = 32

// PresignMultipartPartsCommand names the session whose parts a client without
// a browser session uploads itself.
type PresignMultipartPartsCommand struct {
	FileID   string
	UploadID string
}

// PresignedUploadPart is one short-lived S3 UploadPart URL.
type PresignedUploadPart struct {
	PartNumber int32
	URL        string
	Size       int64
	ExpiresAt  time.Time
}

// PresignMultipartParts returns URLs for the parts object storage does not hold
// yet, for callers that cannot reach the session-authenticated upload routes.
//
// Those routes verify a 64 KiB prefix before signing. Sending that prefix
// through a tool call is impractical, so this records the declared MIME type
// instead and relies on completion: verifyCompletedObjectContent sniffs the
// assembled object against it, then deletes the object and fails the session on
// a mismatch.
func (s *Service) PresignMultipartParts(ctx context.Context, command PresignMultipartPartsCommand) ([]PresignedUploadPart, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}
	session, err := s.loadPresignableSession(ctx, command, user.MemberID.String())
	if err != nil {
		return nil, err
	}
	declared, err := s.declaredMultipartMIME(session)
	if err != nil {
		return nil, err
	}
	objectKey, err := uploadSessionObjectKey(session)
	if err != nil {
		return nil, fmt.Errorf("resolve upload session target: %w", err)
	}
	reconciled, err := s.reconcileWritableMultipartResumeParts(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("reconcile multipart parts: %w", err)
	}
	if reconciled.session.Status == domain.UploadSessionStatusFinalizing {
		return nil, domainerr.Precondition("upload session is already finalizing")
	}
	if err := s.recordDeclaredMultipartMIME(ctx, session, declared); err != nil {
		return nil, err
	}

	uploaded := make(map[int32]struct{}, len(reconciled.parts))
	for _, part := range reconciled.parts {
		uploaded[part.PartNumber] = struct{}{}
	}
	parts := make([]PresignedUploadPart, 0, min(int(session.TotalParts), maxPresignedUploadParts))
	for number := int32(1); number <= session.TotalParts && len(parts) < maxPresignedUploadParts; number++ {
		if _, done := uploaded[number]; done {
			continue
		}
		size := expectedMultipartPartSize(session.FileSize, multipartSessionChunkSize(session), number)
		url, expiresAt, err := s.signMultipartPart(ctx, session, objectKey, number, size)
		if err != nil {
			return nil, fmt.Errorf("sign upload part %d: %w", number, err)
		}
		parts = append(parts, PresignedUploadPart{PartNumber: number, URL: url, Size: size, ExpiresAt: expiresAt})
	}
	return parts, nil
}

func (s *Service) loadPresignableSession(ctx context.Context, command PresignMultipartPartsCommand, memberID string) (domain.UploadSession, error) {
	var session domain.UploadSession
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		session, err = store.UploadSession(ctx, command.UploadID, command.FileID, false)
		return err
	})
	if errors.Is(err, domain.ErrUploadSessionNotFound) {
		return domain.UploadSession{}, domainerr.New(domainerr.NotFound, "", "upload session not found")
	}
	if err != nil {
		return domain.UploadSession{}, fmt.Errorf("load upload session: %w", err)
	}
	if err := s.checkPartUploadPermission(ctx, memberID, session); err != nil {
		return domain.UploadSession{}, err
	}
	if !uploadSessionUsesDirectS3(session) {
		return domain.UploadSession{}, domainerr.Precondition("this upload type must use the authenticated API relay")
	}
	return session, nil
}

// declaredMultipartMIME is the session's requested MIME type when the upload
// type permits it. Direct audio and video need browser-prepared derivatives
// that a part URL cannot provide, so they stop here instead of at completion.
func (s *Service) declaredMultipartMIME(session domain.UploadSession) (string, error) {
	value, ok := domain.ParseUploadType(session.UploadType)
	if !ok {
		return "", fmt.Errorf("upload session contains unsupported upload type %q", session.UploadType)
	}
	uploadType := UploadType(value)
	config := s.getUploadConfig(uploadType)
	if config == nil {
		return "", domainerr.InvalidField("upload_type", "upload type has no MIME policy")
	}
	allowed := buildAllowedMimeSet(config.PermittedMimeTypes)
	declared := canonicalizeMimeType(session.RequestedMime, allowed)
	if _, permitted := allowed[declared]; !permitted {
		return "", domainerr.InvalidField("mime_type", "MIME type is not allowed for this upload type")
	}
	if supportsClientMedia(uploadType) && (strings.HasPrefix(declared, "audio/") || strings.HasPrefix(declared, "video/")) {
		return "", domainerr.Precondition("clientMediaBundleRequired: direct audio and video uploads require completed browser media")
	}
	return declared, nil
}

func (s *Service) recordDeclaredMultipartMIME(ctx context.Context, session domain.UploadSession, declared string) error {
	err := s.ingestLocks.Multipart(ctx, session.UploadID, session.FileID, func(scope domain.IngestSession) error {
		lockedService, err := s.ingestService(scope)
		if err != nil {
			return err
		}
		now := time.Now()
		claimed, err := lockedService.claimUploadPartActivity(ctx, session.UploadID, session.FileID, now)
		if err != nil {
			return err
		}
		if !claimed {
			return errMultipartUploadPartNotWritable
		}
		return lockedService.recordMultipartDetectedMIME(ctx, session, declared, now)
	})
	if errors.Is(err, errMultipartUploadPartNotWritable) {
		return domainerr.Precondition("upload session is no longer writable")
	}
	if err != nil {
		return fmt.Errorf("record declared MIME type: %w", err)
	}
	return nil
}

func (s *Service) signMultipartPart(ctx context.Context, session domain.UploadSession, objectKey string, partNumber int32, size int64) (string, time.Time, error) {
	expiresAt := time.Now().Add(multipartPresignTTL)
	presigned, err := s.s3PresignClient.PresignUploadPart(ctx,
		&s3.UploadPartInput{
			Bucket:        aws.String(s.s3Bucket),
			Key:           aws.String(objectKey),
			UploadId:      aws.String(session.UploadID),
			PartNumber:    aws.Int32(partNumber),
			ContentLength: aws.Int64(size),
		},
		func(options *s3.PresignOptions) { options.Expires = multipartPresignTTL },
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return presigned.URL, expiresAt, nil
}

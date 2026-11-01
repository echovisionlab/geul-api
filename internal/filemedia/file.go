package filemedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	chunkSize             = 10 * 1024 * 1024
	multipartSniffBytes   = 64 * 1024
	partUploadBodySlack   = 1024
	multipartResumeWindow = 7 * 24 * time.Hour
	multipartPresignTTL   = 5 * time.Minute
)

func generalFileMaxSize(mimeType string) int64 {
	mimeType = canonicalMimeType(mimeType)
	switch {
	case strings.HasPrefix(mimeType, "video/"):
		return 8 * 1024 * 1024 * 1024
	case strings.HasPrefix(mimeType, "audio/"):
		return 4 * 1024 * 1024 * 1024
	case strings.HasPrefix(mimeType, "image/"):
		return domain.ManagedRasterFinalMaxSize
	case strings.HasPrefix(mimeType, "model/"):
		return 50 * 1024 * 1024
	default:
		return 500 * 1024 * 1024
	}
}

var (
	errCompletedObjectMetadataMismatch = errors.New("completed object metadata mismatch")
	errStoredObjectMIMEMismatch        = errors.New("stored object MIME mismatch")
)

const siteEmailLogoStableMime = "image/png"

func optionalNonEmptyString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	next := value
	return &next
}

func IsMissingMultipartUploadAbortError(err error) bool {
	if err == nil {
		return false
	}

	var noSuchUpload *types.NoSuchUpload
	if errors.As(err, &noSuchUpload) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NoSuchUpload"
	}

	return false
}

func isMissingStoredObjectError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchKey", "404":
		return true
	default:
		return false
	}
}

func cloneUploadConfigs() map[string]*domain.UploadConfig {
	configs := make(map[string]*domain.UploadConfig, len(domain.DefaultUploadConfigs))
	for uploadType, cfg := range domain.DefaultUploadConfigs {
		if cfg == nil {
			continue
		}
		cloned := *cfg
		cloned.PermittedMimeTypes = append([]string(nil), cfg.PermittedMimeTypes...)
		configs[uploadType] = &cloned
	}
	return configs
}

// getUploadConfig returns the upload config for the given type from DefaultUploadConfigs.
func getUploadConfig(uploadType managev1.UploadType) *domain.UploadConfig {
	if cfg, ok := domain.DefaultUploadConfigs[uploadType.String()]; ok {
		return cfg
	}
	return nil
}

// LegalRouteIdentity resolves stable Legal route IDs for Legal-owned site
// assets. FileMedia owns this consumer port; composition supplies the Legal
// adapter.
type LegalRouteIdentity interface {
	RouteID(kind string) string
}

func (s *Service) getUploadConfig(uploadType managev1.UploadType) *domain.UploadConfig {
	if s != nil && s.uploadConfigs != nil {
		if cfg, ok := s.uploadConfigs[uploadType.String()]; ok {
			return cfg
		}
	}
	return getUploadConfig(uploadType)
}

func (s *Service) resolveTrackReleaseID(ctx context.Context, trackID string) (string, error) {
	var releaseID string
	err := s.readUploadFacts(ctx, func(store Store) error {
		rows, err := store.TrackReleaseIDs(ctx, []string{trackID})
		if err != nil {
			return err
		}
		var ok bool
		releaseID, ok = rows[trackID]
		if !ok {
			return domain.ErrUploadTrackNotFound
		}
		return nil
	})
	return releaseID, err
}

func (s *Service) readUploadFacts(ctx context.Context, read func(Store) error) error {
	if s.uploadReads != nil {
		return s.uploadReads.Read(ctx, read)
	}
	return s.units.Read(ctx, read)
}

func (s *Service) buildManagedFileKey(
	fileID string,
	mimeType string,
) (string, error) {
	return mediaauth.MediaObjectKey(fileID, mediaExtension(&mimeType))
}

func isSiteEmailLogoUpload(uploadType managev1.UploadType, slotID string) bool {
	if uploadType != managev1.UploadType_UPLOAD_TYPE_SITE_LOGO {
		return false
	}
	slot := strings.TrimSpace(slotID)
	return slot == siteLogoSlotEmailLight || slot == siteLogoSlotEmailDark
}

func validateSiteEmailLogoMime(uploadType managev1.UploadType, slotID string, mimeType string) error {
	if !isSiteEmailLogoUpload(uploadType, slotID) {
		return nil
	}
	if normalizeMimeType(mimeType) == siteEmailLogoStableMime {
		return nil
	}
	return domainerr.InvalidField("mime_type", "email logo uploads must be PNG")
}

func validateMultipartCompletionVerifiedMime(
	uploadType managev1.UploadType,
	session domain.UploadSession,
	config *domain.UploadConfig,
) (string, error) {
	if session.DetectedMime == nil || *session.DetectedMime == "" {
		return "", domainerr.Precondition("MIME type not verified")
	}
	if config == nil {
		return "", domainerr.InvalidField("upload_type", fmt.Sprintf("invalid upload type: %s", session.UploadType))
	}

	verifiedMime, err := validateMultipartCompletionMime(
		session.RequestedMime,
		*session.DetectedMime,
		config.PermittedMimeTypes,
	)
	if err != nil {
		return "", err
	}
	if err := validateSiteEmailLogoMime(uploadType, derefString(session.SlotID), verifiedMime); err != nil {
		return "", err
	}

	return verifiedMime, nil
}

// fileIngestRecord is the file row a verified ingest creates. A nil sha256
// and empty ingest identifiers are left out of the insert.
type fileIngestRecord struct {
	id              string
	fileName        string
	mimeType        string
	fileSize        int64
	extension       string
	sha256          []byte
	ingestSlotID    string
	ingestAttemptID string
}

func (r fileIngestRecord) file() File {
	file := File{ID: r.id, FileName: r.fileName, MimeType: r.mimeType, FileSize: r.fileSize, Extension: r.extension, SHA256: r.sha256}
	if r.ingestSlotID != "" {
		file.IngestSlotID = new(r.ingestSlotID)
	}
	if r.ingestAttemptID != "" {
		file.IngestAttemptID = new(r.ingestAttemptID)
	}
	return file
}

func completedMultipartFileRecord(session domain.UploadSession) fileIngestRecord {
	extension := mediaExtension(session.DetectedMime)
	return fileIngestRecord{
		id:              session.FileID,
		fileName:        storedFileBasename(session.FileName, session.FileID, extension),
		mimeType:        derefString(session.DetectedMime),
		fileSize:        session.FileSize,
		extension:       extension,
		ingestSlotID:    derefString(session.SlotID),
		ingestAttemptID: derefString(session.AttemptID),
	}
}

func (s *Service) createVerifiedFileIngestRecord(
	ctx context.Context,
	record fileIngestRecord,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
	authority verifiedFileIngestAuthority,
) error {
	fileID := record.id
	if strings.TrimSpace(fileID) == "" {
		return fmt.Errorf("completed file ID is required")
	}
	if authority == nil {
		return fmt.Errorf("verified File ingest authority is required")
	}
	if s.storeMutations == nil {
		return fmt.Errorf("File ingest mutation runner is required")
	}
	_, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[string, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			return authority.revalidate(ctx, store, s, fileID, uploadType, entityType, entityID, lockActive)
		}),
		Change: func(ctx context.Context, store Store) (string, error) {
			file := record.file()
			file.FileName = storedFileBasename(record.fileName, fileID, record.extension)
			folder, err := store.EnsureIngestDateFolder(ctx)
			if err != nil {
				return "", err
			}
			file.FolderID = folder
			if user := auth.GetUser(ctx); user != nil && strings.TrimSpace(user.MemberID.String()) != "" {
				file.UploadedByMemberID = new(user.MemberID.String())
			}
			if err := store.InsertIngestFile(ctx, file); err != nil {
				return "", err
			}
			binding := domain.IngestBinding{FileID: fileID, UploadType: uploadType.String(), EntityID: entityID}
			if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
				binding.EntityType = new(entityType.String())
			}
			if err := store.InsertIngestBinding(ctx, binding, uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE || isEditorFileIngestUploadType(uploadType)); err != nil {
				return "", err
			}
			return fileID, nil
		},
		Audit: fileCreatedAudit,
		Relationships: func(id string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			apply, err := policyv1.File.TouchPolicy(id)
			if err != nil {
				return nil, nil, err
			}
			compensate, err := policyv1.File.DeletePolicy(id)
			if err != nil {
				return nil, nil, err
			}
			return []policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}, nil
		},
	})
	if err != nil {
		return err
	}
	if s.testVerifiedIngestCommitError != nil {
		return s.testVerifiedIngestCommitError
	}
	return nil
}

func (s *Service) completedFileRecordExists(ctx context.Context, fileID string) (bool, error) {
	var count int64
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		count, err = store.FileCount(ctx, []string{fileID})
		return err
	})
	return count == 1, err
}

func (s *Service) validateCompletedFileRecord(
	ctx context.Context,
	session domain.UploadSession,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
) error {
	var file File
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		file, err = store.IngestFile(ctx, session.FileID)
		return err
	}); err != nil {
		return fmt.Errorf("load completed file record: %w", err)
	}

	if file.FileName != storedFileBasename(session.FileName, session.FileID, file.Extension) ||
		file.MimeType != derefString(session.DetectedMime) ||
		file.FileSize != session.FileSize ||
		file.Extension != mediaExtension(session.DetectedMime) ||
		!sameOptionalString(file.IngestSlotID, session.SlotID) ||
		!sameOptionalString(file.IngestAttemptID, session.AttemptID) {
		return fmt.Errorf("completed file record does not match upload session")
	}
	if file.DeleteRequestedAt != nil {
		return fmt.Errorf("completed file is pending deletion")
	}

	var binding domain.IngestBinding
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		binding, err = store.IngestBinding(ctx, session.FileID)
		return err
	}); err != nil {
		return fmt.Errorf("load completed file ingest binding: %w", err)
	}

	var expectedEntityType *string
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		value := entityType.String()
		expectedEntityType = &value
	}
	if binding.UploadType != uploadType.String() ||
		binding.EntityID != session.EntityID ||
		!sameOptionalString(binding.EntityType, expectedEntityType) {
		return fmt.Errorf("completed file ingest binding does not match upload session")
	}

	return nil
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Service) verifyCompletedObjectMetadata(
	ctx context.Context,
	objectKey string,
	expectedSize int64,
	expectedMime string,
) error {
	output, err := s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("head completed object: %w", err)
	}
	if aws.ToInt64(output.ContentLength) != expectedSize {
		return fmt.Errorf("%w: size does not match upload session", errCompletedObjectMetadataMismatch)
	}
	if canonicalMimeType(aws.ToString(output.ContentType)) != canonicalMimeType(expectedMime) {
		return fmt.Errorf("%w: MIME does not match upload session", errCompletedObjectMetadataMismatch)
	}
	return nil
}

func (s *Service) readStoredObjectPrefix(
	ctx context.Context,
	objectKey string,
	expectedSize int64,
) ([]byte, error) {
	prefixSize := min(int64(multipartSniffBytes), expectedSize)
	if prefixSize <= 0 {
		return nil, fmt.Errorf("completed object size must be positive")
	}
	output, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(objectKey),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", prefixSize-1)),
	})
	if err != nil {
		return nil, fmt.Errorf("get completed object prefix: %w", err)
	}
	defer output.Body.Close()

	prefix, err := io.ReadAll(io.LimitReader(output.Body, prefixSize+1))
	if err != nil {
		return nil, fmt.Errorf("read completed object prefix: %w", err)
	}
	if int64(len(prefix)) != prefixSize {
		return nil, fmt.Errorf("completed object prefix size %d does not match expected size %d", len(prefix), prefixSize)
	}
	return prefix, nil
}

// Site logo upload slots. Each slot owns a stable upload entity; the full light,
// full dark and email light slots keep the entities of the logos they replace.
const (
	siteLogoSlotFullLight   = "logo_full_light"
	siteLogoSlotFullDark    = "logo_full_dark"
	siteLogoSlotSymbolLight = "logo_symbol_light"
	siteLogoSlotSymbolDark  = "logo_symbol_dark"
	siteLogoSlotEmailLight  = "logo_email_light"
	siteLogoSlotEmailDark   = "logo_email_dark"
)

const (
	siteUploadEntityIDLogoFullLight    = "00000000-0000-0000-0000-000000000201"
	siteUploadEntityIDLogoEmailLight   = "00000000-0000-0000-0000-000000000202"
	siteUploadEntityIDFavicon          = "00000000-0000-0000-0000-000000000203"
	siteUploadEntityIDLoader           = "00000000-0000-0000-0000-000000000204"
	siteUploadEntityIDSiteOgBackground = "00000000-0000-0000-0000-000000000205"
	siteUploadEntityIDLogoFullDark     = "00000000-0000-0000-0000-000000000206"
	siteUploadEntityIDLogoSymbolLight  = "00000000-0000-0000-0000-000000000207"
	siteUploadEntityIDLogoSymbolDark   = "00000000-0000-0000-0000-000000000208"
	siteUploadEntityIDLogoEmailDark    = "00000000-0000-0000-0000-000000000209"
)

var siteLogoUploadEntityIDs = map[string]string{
	siteLogoSlotFullLight:   siteUploadEntityIDLogoFullLight,
	siteLogoSlotFullDark:    siteUploadEntityIDLogoFullDark,
	siteLogoSlotSymbolLight: siteUploadEntityIDLogoSymbolLight,
	siteLogoSlotSymbolDark:  siteUploadEntityIDLogoSymbolDark,
	siteLogoSlotEmailLight:  siteUploadEntityIDLogoEmailLight,
	siteLogoSlotEmailDark:   siteUploadEntityIDLogoEmailDark,
}

func resolveSiteUploadEntityID(
	legalRoutes LegalRouteIdentity,
	uploadType managev1.UploadType,
	slotID string,
) (string, error) {
	slot := strings.TrimSpace(slotID)

	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_SITE_LOGO:
		if entityID, ok := siteLogoUploadEntityIDs[slot]; ok {
			return entityID, nil
		}
		return "", domainerr.InvalidField("slot_id", fmt.Sprintf("unsupported site logo slot: %s", slot))

	case managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON:
		if slot != "" && slot != "favicon" {
			return "", domainerr.InvalidField("slot_id", fmt.Sprintf("unsupported site favicon slot: %s", slot))
		}
		return siteUploadEntityIDFavicon, nil

	case managev1.UploadType_UPLOAD_TYPE_SITE_LOADER:
		if slot != "" && slot != "loader" {
			return "", domainerr.InvalidField("slot_id", fmt.Sprintf("unsupported site loader slot: %s", slot))
		}
		return siteUploadEntityIDLoader, nil

	case managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND:
		switch slot {
		case "", "site_og_background":
			return siteUploadEntityIDSiteOgBackground, nil
		case "privacy_og_background":
			if legalRoutes == nil {
				return "", domainerr.DependencyUnavailable("Legal route identity")
			}
			return legalRoutes.RouteID("privacy"), nil
		case "terms_og_background":
			if legalRoutes == nil {
				return "", domainerr.DependencyUnavailable("Legal route identity")
			}
			return legalRoutes.RouteID("terms"), nil
		default:
			return "", domainerr.InvalidField("slot_id", fmt.Sprintf("unsupported site OG background slot: %s", slot))
		}
	}

	return "", nil
}

// checkPartUploadPermission verifies that the user still has permission at
// prefix verification, presign, and confirmation. A signed part URL itself is
// intentionally independent of the application session until its short expiry.
func (s *Service) checkPartUploadPermission(ctx context.Context, userID string, session domain.UploadSession) error {
	if session.ClientMediaBundleID != nil {
		plan, err := decodeClientMediaPlan(session)
		if err != nil {
			return err
		}
		if plan.MemberID != userID {
			return domainerr.Denied("client media bundle belongs to another member")
		}
	}
	uploadType := managev1.UploadType(managev1.UploadType_value[session.UploadType])
	target, entityID, err := s.resolvePartUploadPermissionTarget(ctx, uploadType, session)
	if err != nil {
		return err
	}
	if err := s.validatePartUploadTarget(ctx, uploadType, target, entityID); err != nil {
		return err
	}
	handled, err := s.checkMemberAuthorityUploadTarget(ctx, target, entityID, userID)
	if handled {
		return err
	}
	return s.checkRoleAndSpiceDBUploadPermission(ctx, target, entityID, userID)
}

// checkEntityPermission verifies that the user has permission to upload to the specified entity.
// This prevents unauthorized users from uploading files to entities they don't manage.
func (s *Service) checkEntityPermission(ctx context.Context, uploadType managev1.UploadType, entityType, entityID, userID string) error {
	user := auth.GetUser(ctx)
	if user == nil {
		return domainerr.AuthenticationRequired()
	}

	entityID = strings.TrimSpace(entityID)
	target, err := resolveUploadPermissionTarget(uploadType, entityType)
	if err != nil {
		return connectUploadPermissionValidationError(err)
	}
	if uploadPermissionRequiresEntityID(uploadType, target) && entityID == "" {
		return domainerr.Required("entity_id")
	}
	entityID, err = s.resolveUploadPermissionEntityID(ctx, uploadType, entityID)
	if err != nil {
		return err
	}
	if uploadPermissionRequiresEntityID(uploadType, target) && strings.TrimSpace(entityID) == "" {
		return domainerr.Required("entity_id")
	}
	if target.resourceType != "post" && target.resourceType != "program_event" {
		if err := s.ensureUploadPermissionEntityExists(ctx, target, entityID); err != nil {
			return err
		}
	}
	if target.resourceType == "post" {
		return s.requireOwnerPreflight(ctx, "post", entityID, true)
	}
	if target.resourceType == "program_event" {
		return s.requireOwnerPreflight(ctx, "program_event", entityID, true)
	}
	if handled, err := s.checkUploadResourceAuthority(ctx, target, entityID, user); handled {
		return err
	}
	if handled, err := checkUploadPrincipalPolicy(ctx, target, entityID, userID, user, s.spiceDB); handled {
		return err
	}
	return s.checkSpiceDBUploadPermission(ctx, target, entityID)
}

func (s *Service) resolveUploadPermissionEntityID(
	ctx context.Context,
	uploadType managev1.UploadType,
	entityID string,
) (string, error) {
	if uploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO {
		return entityID, nil
	}
	releaseID, err := s.resolveTrackReleaseID(ctx, entityID)
	if errors.Is(err, domain.ErrUploadTrackNotFound) {
		return "", domainerr.New(domainerr.NotFound, "", "track not found")
	}
	if err != nil {
		return "", fmt.Errorf("failed to resolve track release: %w", err)
	}
	return releaseID, nil
}

func (s *Service) checkUploadResourceAuthority(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	user *auth.UserInfo,
) (bool, error) {
	// SpiceDB is the sole authorization authority. The database-backed
	// Post/Artist/Label authority projections are intentionally not consulted
	// here; they can be stale relative to the typed relation graph.
	return false, nil
}

func checkUploadPrincipalPolicy(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	userID string,
	user *auth.UserInfo,
	spiceDB authz.AuthorizationDecisionChecker,
) (bool, error) {
	adminCan, err := policyv1.File.ManageLibrary()
	if err != nil {
		return true, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, adminCan, spiceDB)
	if err != nil {
		return true, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if isAdmin {
		return true, nil
	}
	if target.authorOnly {
		authorCan, err := policyv1.File.List()
		if err != nil {
			return true, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		isAuthor, err := checkSpiceDBCan(ctx, user, authorCan, spiceDB)
		if err != nil {
			return true, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		if !isAuthor {
			return true, domainerr.NoPermission("upload to", "the file manager")
		}
	}
	if target.userOwned {
		if entityID != userID {
			return true, domainerr.Denied("cannot upload avatar for another user")
		}
		return true, nil
	}
	if target.adminOnly {
		return true, domainerr.AdminRequired()
	}
	return false, nil
}

func (s *Service) checkSpiceDBUploadPermission(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
) error {
	if !target.requiresSpiceDBCheck || target.resourceType == "" || entityID == "" {
		return nil
	}
	if err := requireUploadIdentity(ctx); err != nil {
		return err
	}
	can, err := uploadPermissionEditCan(target.resourceType, entityID)
	if err != nil {
		if errors.Is(err, errUnsupportedUploadAuthorizationResource) {
			return domainerr.InvalidField("entity_type", "unsupported authorization resource")
		}
		return domainerr.InvalidField("entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	hasPermission, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return fmt.Errorf("permission check failed: %w", failurecause.WithAttrs(err, slog.String("resource_type", target.resourceType), slog.String("entity_id", entityID)))
	}
	if !hasPermission {
		return domainerr.NoPermission("upload to", "this entity")
	}
	return nil
}

func (s *Service) ensureUploadPermissionEntityExists(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
) error {
	if !target.requiresSpiceDBCheck || target.resourceType == "" || strings.TrimSpace(entityID) == "" {
		return nil
	}

	if _, ok := uploadPermissionResourceTable(target.resourceType); !ok {
		return nil
	}
	var exists bool
	if err := s.readUploadFacts(ctx, func(store Store) error {
		var err error
		exists, err = store.UploadOwnerExists(ctx, target.resourceType, entityID)
		return err
	}); err != nil {
		return err
	}
	if !exists {
		return domainerr.ResourceNotFound(target.resourceType, entityID)
	}

	return nil
}

func uploadPermissionResourceTable(resourceType string) (string, bool) {
	switch resourceType {
	case "artist":
		return "artist", true
	case "form":
		return "form", true
	case "label":
		return "label", true
	case "page":
		return "page", true
	case "post":
		return "post", true
	case "program_event":
		return "program_event", true
	case "release":
		return "release", true
	case "series":
		return "series", true
	case "work":
		return "work", true
	default:
		return "", false
	}
}

// AbortMultipartUpload aborts a multipart upload
// AbortMultipartUploadCommand aborts the multipart upload of a File.
type AbortMultipartUploadCommand struct {
	FileID        string
	UploadID      string
	CorrelationID string
}

func (s *Service) AbortMultipartUpload(ctx context.Context, command AbortMultipartUploadCommand) error {
	var row domain.UploadSession
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		row, err = store.UploadSession(ctx, command.UploadID, command.FileID, false)
		return err
	}); err != nil {
		if errors.Is(err, domain.ErrUploadSessionNotFound) {
			return domainerr.New(domainerr.NotFound, "", "upload session not found")
		}
		return fmt.Errorf("failed to load upload session: %w", err)
	}
	session := row

	user := auth.GetUser(ctx)
	if user == nil {
		return domainerr.AuthenticationRequired()
	}
	entityType := ""
	if session.EntityType != nil {
		entityType = *session.EntityType
	}
	uploadType := managev1.UploadType(managev1.UploadType_value[session.UploadType])
	if err := s.checkEntityPermission(ctx, uploadType, entityType, session.EntityID, user.MemberID.String()); err != nil {
		return err
	}

	progressEmitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(session.EntityType),
		session.EntityID,
		command.CorrelationID,
		session.FileID,
		session.FileSize,
	)
	if progressEmitter != nil {
		if err := s.bindUploadSessionIngestEmitter(progressEmitter, session); err != nil {
			return err
		}
	}
	if session.ClientMediaBundleID != nil {
		if err := s.checkPartUploadPermission(ctx, user.MemberID.String(), session); err != nil {
			return err
		}
	}
	fileKey, err := uploadSessionObjectKey(session)
	if err != nil {
		return fmt.Errorf("invalid upload session target: %w", err)
	}
	claimed, err := s.claimUploadSessionAbort(ctx, session.UploadID, session.FileID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to claim upload session abort: %w", err)
	}
	if !claimed {
		return domainerr.Precondition("upload session is finalizing and can no longer be aborted")
	}

	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()
	_, err = s.s3Client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.s3Bucket),
		Key:      aws.String(fileKey),
		UploadId: aws.String(session.UploadID),
	})
	if err != nil {
		if !IsMissingMultipartUploadAbortError(err) {
			return fmt.Errorf("failed to abort multipart upload: %w", err)
		}
		slog.Info("Multipart upload was already absent during abort cleanup", "uploadId", session.UploadID, "fileId", session.FileID)
	}

	if progressEmitter != nil {
		progressEmitter.publishFailed("Upload aborted", 0, nil)
	}

	if err := s.cleanupClientMediaStaging(cleanupCtx, session); err != nil {
		return err
	}
	if err := s.deleteAbortedUploadSession(cleanupCtx, session.UploadID); err != nil {
		return fmt.Errorf("failed to delete upload session after abort: %w", err)
	}

	return nil
}

// FindMultipartUploadCandidateQuery describes the upload a client may resume.
// The optional fields narrow the match to the same local file.
type FindMultipartUploadCandidateQuery struct {
	UploadType            managev1.UploadType
	EntityID              string
	EntityType            *managev1.TranscodeEntityType
	FileID                *string
	UploadID              *string
	FileName              *string
	FileSize              *int64
	MimeType              *string
	FileLastModified      *int64
	SlotID                *string
	ExpectedCurrentFileID *string
}

func (q FindMultipartUploadCandidateQuery) entityType() managev1.TranscodeEntityType {
	if q.EntityType == nil {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
	}
	return *q.EntityType
}

// FindMultipartUploadCandidate returns the latest active resumable upload for
// an entity and upload type, or an empty response when there is none.
func (s *Service) FindMultipartUploadCandidate(
	ctx context.Context,
	query FindMultipartUploadCandidateQuery,
) (*MultipartCandidate, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}

	search, err := s.prepareMultipartCandidateSearch(ctx, query, user.MemberID.String())
	if err != nil {
		return nil, err
	}
	session, found, err := s.findMultipartUploadCandidate(ctx, search)
	if err != nil {
		return nil, fmt.Errorf("failed to find upload session candidate: %w", err)
	}
	if !found {
		return &MultipartCandidate{}, nil
	}
	response, err := s.prepareMultipartCandidateResponse(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile upload session candidate: %w", err)
	}
	return response, nil
}

package filemedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
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
		return model.ManagedRasterFinalMaxSize
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

const (
	siteEmailLogoSlotID     = "logo_email"
	siteEmailLogoStableMime = "image/png"
)

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

func cloneUploadConfigs() map[managev1.UploadType]*model.UploadConfig {
	configs := make(map[managev1.UploadType]*model.UploadConfig, len(model.DefaultUploadConfigs))
	for uploadType, cfg := range model.DefaultUploadConfigs {
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
func getUploadConfig(uploadType managev1.UploadType) *model.UploadConfig {
	if cfg, ok := model.DefaultUploadConfigs[uploadType]; ok {
		return cfg
	}
	return nil
}

type FileServiceOption func(*FileService)

// LegalRouteIdentity resolves stable Legal route IDs for Legal-owned site
// assets. FileMedia owns this consumer port; composition supplies the Legal
// adapter.
type LegalRouteIdentity interface {
	RouteID(kind string) string
}

func WithEditorImageMaxSizeBytes(maxSize int64) FileServiceOption {
	return func(s *FileService) {
		if maxSize <= 0 {
			return
		}
		if cfg := s.uploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE]; cfg != nil {
			cfg.MaxSize = maxSize
		}
	}
}

func WithMediaDownloadTTL(downloadTTL time.Duration) FileServiceOption {
	return func(s *FileService) {
		if downloadTTL > 0 {
			s.downloadTTL = min(downloadTTL, mediaauth.DownloadTTL)
		}
	}
}

func WithFileDomainAuditWriter(writer domainaudit.Appender) FileServiceOption {
	return func(s *FileService) { s.auditWriter = writer }
}

func WithMultipartUploadPresigner(presigner *s3.PresignClient) FileServiceOption {
	return func(s *FileService) {
		if presigner != nil {
			s.s3PresignClient = presigner
		}
	}
}

func WithLegalRouteIdentity(identity LegalRouteIdentity) FileServiceOption {
	return func(s *FileService) { s.legalRouteIdentity = identity }
}

func WithPostAccess(access PostAccess) FileServiceOption {
	return func(s *FileService) { s.postAccess = access }
}

func WithPagePolicyAccess(access PagePolicyAccess) FileServiceOption {
	return func(s *FileService) { s.pagePolicyAccess = access }
}

func WithWorkAttachment(attachment WorkAttachment) FileServiceOption {
	return func(s *FileService) { s.workAttachment = attachment }
}

func WithWorkPolicyAccess(access WorkPolicyAccess) FileServiceOption {
	return func(s *FileService) { s.workPolicyAccess = access }
}

func WithProgramEventAttachment(attachment ProgramEventAttachment) FileServiceOption {
	return func(s *FileService) { s.programEventAttachment = attachment }
}

func WithTrackAttachment(attachment TrackAttachment) FileServiceOption {
	return func(s *FileService) { s.trackAttachment = attachment }
}

func WithReleasePolicyAccess(access ReleasePolicyAccess) FileServiceOption {
	return func(s *FileService) { s.releasePolicyAccess = access }
}

func WithAudienceAccess(access AudienceAccess) FileServiceOption {
	return func(s *FileService) { s.audienceAccess = access }
}

func WithMemberSummaries(summaries MemberSummaries) FileServiceOption {
	return func(s *FileService) { s.memberSummaries = summaries }
}

// FileService implements the FileService Connect handler
type FileService struct {
	managev1connect.UnimplementedFileServiceHandler
	db                        *gorm.DB
	s3Bucket                  string
	cdnDomain                 string
	mediaDomain               string
	mediaSecret               string
	downloadTTL               time.Duration
	s3Client                  *s3.Client
	s3PresignClient           *s3.PresignClient
	asyncPublisher            AsyncPublisher
	publisher                 mq.TranscoderPublisher
	spiceDB                   *auth.SpiceDBClient
	auditWriter               domainaudit.Appender
	postAccess                PostAccess
	pagePolicyAccess          PagePolicyAccess
	workAttachment            WorkAttachment
	workPolicyAccess          WorkPolicyAccess
	programEventAttachment    ProgramEventAttachment
	trackAttachment           TrackAttachment
	releasePolicyAccess       ReleasePolicyAccess
	audienceAccess            AudienceAccess
	memberSummaries           MemberSummaries
	remoteImportResolver      remoteImportResolver
	remoteImportDialer        remoteImportDialer
	remoteImportBaseTransport *http.Transport
	legalRouteIdentity        LegalRouteIdentity

	uploadConfigs                      map[managev1.UploadType]*model.UploadConfig
	faviconProcessor                   favicon.Processor
	testFaviconBundleTimeout           time.Duration
	testFaviconCommitError             error
	testFaviconReconcileError          error
	testVerifiedIngestCommitError      error
	testBeforeTrackSessionInsert       func(model.UploadSession)
	testAfterTrackSessionInsert        func(model.UploadSession)
	testBeforeResumeReconciliation     func(model.UploadSession)
	testBeforeFileManagerDeliveryFence func([]string)
	testAfterFileManagerDeliverySigned func([]string)
	testBeforeManageDeliveryFence      func([]string)
	testBeforeManagePrincipalLock      func([]string)
	testAfterManageDeliverySigned      func([]string)
	testBeforeFileMutationPrincipal    func(*gorm.DB, []string)
	testAfterFileMutationPrincipal     func([]string)
	testAfterScopedFeaturedSigned      func(string, string, string)
	testAfterScopedBlockMediaSigned    func(string, string, []string)
}

// NewFileService creates a new FileService
func NewFileService(
	db *gorm.DB,
	s3Client *s3.Client,
	asyncPublisher AsyncPublisher,
	s3Bucket, cdnDomain, mediaDomain string,
	mediaSecret string,
	publisher mq.TranscoderPublisher,
	spiceDB *auth.SpiceDBClient,
	options ...FileServiceOption,
) *FileService {
	if db == nil {
		panic("db is required")
	}
	if s3Client == nil {
		panic("s3Client is required")
	}
	if asyncPublisher == nil {
		panic("asyncPublisher is required")
	}
	if publisher == nil {
		panic("publisher is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}

	s := &FileService{
		db:               db,
		s3Client:         s3Client,
		s3PresignClient:  s3.NewPresignClient(s3Client),
		asyncPublisher:   asyncPublisher,
		s3Bucket:         s3Bucket,
		cdnDomain:        cdnDomain,
		mediaDomain:      mediaDomain,
		mediaSecret:      mediaSecret,
		downloadTTL:      mediaauth.DownloadTTL,
		publisher:        publisher,
		spiceDB:          spiceDB,
		uploadConfigs:    cloneUploadConfigs(),
		faviconProcessor: favicon.NewProcessor(),
	}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}

func (s *FileService) getUploadConfig(uploadType managev1.UploadType) *model.UploadConfig {
	if s != nil && s.uploadConfigs != nil {
		if cfg, ok := s.uploadConfigs[uploadType]; ok {
			return cfg
		}
	}
	return getUploadConfig(uploadType)
}

func (s *FileService) resolveTrackReleaseID(ctx context.Context, trackID string) (string, error) {
	var track struct {
		ReleaseID string `gorm:"column:release_id"`
	}

	if err := s.db.WithContext(ctx).
		Table("track").
		Select("release_id").
		Where("id = ?", trackID).
		First(&track).Error; err != nil {
		return "", err
	}

	return track.ReleaseID, nil
}

func (s *FileService) buildManagedFileKey(
	fileID string,
	mimeType string,
) (string, error) {
	return mediaauth.MediaObjectKey(fileID, mediaExtension(&mimeType))
}

func isSiteEmailLogoUpload(uploadType managev1.UploadType, slotID string) bool {
	return uploadType == managev1.UploadType_UPLOAD_TYPE_SITE_LOGO &&
		strings.TrimSpace(slotID) == siteEmailLogoSlotID
}

func validateSiteEmailLogoMime(uploadType managev1.UploadType, slotID string, mimeType string) error {
	if !isSiteEmailLogoUpload(uploadType, slotID) {
		return nil
	}
	if normalizeMimeType(mimeType) == siteEmailLogoStableMime {
		return nil
	}
	return errs.InvalidArgument("mime_type", "email logo uploads must be PNG")
}

func validateMultipartCompletionVerifiedMime(
	uploadType managev1.UploadType,
	session model.UploadSession,
	config *model.UploadConfig,
) (string, error) {
	if session.DetectedMime == nil || *session.DetectedMime == "" {
		return "", errs.FailedPrecondition(errs.MsgMimeNotVerified)
	}
	if config == nil {
		return "", errs.InvalidArgument("upload_type", fmt.Sprintf("invalid upload type: %s", session.UploadType))
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

func completedMultipartFileRecord(session model.UploadSession) structured.Fields {
	extension := mediaExtension(session.DetectedMime)
	file := structured.Fields{
		"id":        session.FileID,
		"file_name": storedFileBasename(session.FileName, session.FileID, extension),
		"mime_type": derefString(session.DetectedMime),
		"file_size": session.FileSize,
		"extension": extension,
	}
	if session.SlotID != nil && *session.SlotID != "" {
		file["ingest_slot_id"] = *session.SlotID
	}
	if session.AttemptID != nil && *session.AttemptID != "" {
		file["ingest_attempt_id"] = *session.AttemptID
	}
	return file
}

func ensureFileManagerDateFolder(ctx context.Context, tx *gorm.DB) (*string, error) {
	if tx.Dialector.Name() != "postgres" {
		return nil, nil
	}
	var dateParts struct {
		Year  string `gorm:"column:year"`
		Month string `gorm:"column:month"`
		Day   string `gorm:"column:day"`
	}
	if err := tx.WithContext(ctx).Raw(`
		SELECT to_char(CURRENT_DATE, 'YYYY') AS year,
		       to_char(CURRENT_DATE, 'MM') AS month,
		       to_char(CURRENT_DATE, 'DD') AS day
	`).Scan(&dateParts).Error; err != nil {
		return nil, err
	}
	var parentID *string
	for _, name := range []string{dateParts.Year, dateParts.Month, dateParts.Day} {
		var folderID string
		if err := tx.WithContext(ctx).Raw(`
			INSERT INTO file_folder (id, parent_id, name, created_by_member_id, created_at, updated_at)
			VALUES (gen_random_uuid(), ?, ?, NULL, now(), now())
			ON CONFLICT (parent_id, name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id
		`, parentID, name).Scan(&folderID).Error; err != nil {
			return nil, err
		}
		parentID = &folderID
	}
	return parentID, nil
}

func (s *FileService) createVerifiedFileIngestRecord(
	ctx context.Context,
	file structured.Fields,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
	authority verifiedFileIngestAuthority,
) error {
	fileID, _ := file["id"].(string)
	if strings.TrimSpace(fileID) == "" {
		return fmt.Errorf("completed file ID is required")
	}
	if authority == nil {
		return fmt.Errorf("verified File ingest authority is required")
	}
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := authority.revalidate(ctx, tx, s, fileID, uploadType, entityType, entityID); err != nil {
			return err
		}
		extension, _ := file["extension"].(string)
		fileName, _ := file["file_name"].(string)
		file["file_name"] = storedFileBasename(fileName, fileID, extension)
		folderID, err := ensureFileManagerDateFolder(ctx, tx)
		if err != nil {
			return err
		}
		if folderID != nil {
			file["folder_id"] = *folderID
		}
		if user := auth.GetUser(ctx); user != nil && strings.TrimSpace(user.MemberID.String()) != "" {
			file["uploaded_by_member_id"] = user.MemberID.String()
		}
		if err := tx.Table("file").Create(file).Error; err != nil {
			return err
		}
		var entityTypeName *string
		if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
			value := entityType.String()
			entityTypeName = &value
		}
		binding := &model.FileIngestBinding{
			FileID:     fileID,
			UploadType: uploadType.String(),
			EntityType: entityTypeName,
			EntityID:   entityID,
		}
		bindingWrite := tx
		if uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE || isEditorFileIngestUploadType(uploadType) {
			bindingWrite = bindingWrite.Omit("EntityID")
		}
		if err := bindingWrite.Create(binding).Error; err != nil {
			return err
		}
		if err := appendFileCreatedAudit(ctx, tx, s.auditWriter, fileID); err != nil {
			return err
		}
		apply, err := policyv1.File.TouchPolicy(fileID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.File.DeletePolicy(fileID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		return err
	}
	if s.testVerifiedIngestCommitError != nil {
		return s.testVerifiedIngestCommitError
	}
	return nil
}

func (s *FileService) completedFileRecordExists(ctx context.Context, fileID string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.File{}).
		Where("id = ?", fileID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *FileService) validateCompletedFileRecord(
	ctx context.Context,
	session model.UploadSession,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
) error {
	var file model.File
	if err := s.db.WithContext(ctx).Where("id = ?", session.FileID).Take(&file).Error; err != nil {
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

	var binding model.FileIngestBinding
	if err := s.db.WithContext(ctx).Where("file_id = ?", session.FileID).Take(&binding).Error; err != nil {
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

func (s *FileService) verifyCompletedObjectMetadata(
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

func (s *FileService) readStoredObjectPrefix(
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

const (
	siteUploadEntityIDLogoLight        = "00000000-0000-0000-0000-000000000201"
	siteUploadEntityIDEmailLogo        = "00000000-0000-0000-0000-000000000202"
	siteUploadEntityIDFavicon          = "00000000-0000-0000-0000-000000000203"
	siteUploadEntityIDLoader           = "00000000-0000-0000-0000-000000000204"
	siteUploadEntityIDSiteOgBackground = "00000000-0000-0000-0000-000000000205"
	siteUploadEntityIDLogoDark         = "00000000-0000-0000-0000-000000000206"
)

func resolveSiteUploadEntityID(
	legalRoutes LegalRouteIdentity,
	uploadType managev1.UploadType,
	slotID string,
) (string, error) {
	slot := strings.TrimSpace(slotID)

	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_SITE_LOGO:
		switch slot {
		case "", "logo", "logo_light":
			return siteUploadEntityIDLogoLight, nil
		case "logo_dark":
			return siteUploadEntityIDLogoDark, nil
		case "logo_email":
			return siteUploadEntityIDEmailLogo, nil
		default:
			return "", errs.InvalidArgument("slot_id", fmt.Sprintf("unsupported site logo slot: %s", slot))
		}

	case managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON:
		if slot != "" && slot != "favicon" {
			return "", errs.InvalidArgument("slot_id", fmt.Sprintf("unsupported site favicon slot: %s", slot))
		}
		return siteUploadEntityIDFavicon, nil

	case managev1.UploadType_UPLOAD_TYPE_SITE_LOADER:
		if slot != "" && slot != "loader" {
			return "", errs.InvalidArgument("slot_id", fmt.Sprintf("unsupported site loader slot: %s", slot))
		}
		return siteUploadEntityIDLoader, nil

	case managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND:
		switch slot {
		case "", "site_og_background":
			return siteUploadEntityIDSiteOgBackground, nil
		case "privacy_og_background":
			if legalRoutes == nil {
				return "", errs.DependencyUnavailable("Legal route identity")
			}
			return legalRoutes.RouteID("privacy"), nil
		case "terms_og_background":
			if legalRoutes == nil {
				return "", errs.DependencyUnavailable("Legal route identity")
			}
			return legalRoutes.RouteID("terms"), nil
		default:
			return "", errs.InvalidArgument("slot_id", fmt.Sprintf("unsupported site OG background slot: %s", slot))
		}
	}

	return "", nil
}

// checkPartUploadPermission verifies that the user still has permission at
// prefix verification, presign, and confirmation. A signed part URL itself is
// intentionally independent of the application session until its short expiry.
func (s *FileService) checkPartUploadPermission(ctx context.Context, userID string, session model.UploadSession) error {
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
func (s *FileService) checkEntityPermission(ctx context.Context, uploadType managev1.UploadType, entityType, entityID, userID string) error {
	user := auth.GetUser(ctx)
	if user == nil {
		return errs.AuthenticationRequired()
	}

	entityID = strings.TrimSpace(entityID)
	target, err := resolveUploadPermissionTarget(uploadType, entityType)
	if err != nil {
		return connectUploadPermissionValidationError(err)
	}
	if uploadPermissionRequiresEntityID(uploadType, target) && entityID == "" {
		return errs.Required("entity_id")
	}
	entityID, err = s.resolveUploadPermissionEntityID(ctx, uploadType, entityID)
	if err != nil {
		return err
	}
	if uploadPermissionRequiresEntityID(uploadType, target) && strings.TrimSpace(entityID) == "" {
		return errs.Required("entity_id")
	}
	if target.resourceType != "post" && target.resourceType != "program_event" {
		if err := s.ensureUploadPermissionEntityExists(ctx, target, entityID); err != nil {
			return err
		}
	}
	if target.resourceType == "post" {
		access, dependencyErr := requirePostAccess(s.postAccess)
		if dependencyErr != nil {
			return dependencyErr
		}
		return access.RequireEdit(ctx, entityID)
	}
	if target.resourceType == "program_event" {
		access, dependencyErr := requireProgramEventAttachment(s.programEventAttachment)
		if dependencyErr != nil {
			return dependencyErr
		}
		return access.RequireEdit(ctx, s.spiceDB, entityID)
	}
	if handled, err := s.checkUploadResourceAuthority(ctx, target, entityID, user); handled {
		return err
	}
	if handled, err := checkUploadPrincipalPolicy(ctx, target, entityID, userID, user, s.spiceDB); handled {
		return err
	}
	return s.checkSpiceDBUploadPermission(ctx, target, entityID, userID)
}

func (s *FileService) resolveUploadPermissionEntityID(
	ctx context.Context,
	uploadType managev1.UploadType,
	entityID string,
) (string, error) {
	if uploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO {
		return entityID, nil
	}
	releaseID, err := s.resolveTrackReleaseID(ctx, entityID)
	if err == gorm.ErrRecordNotFound {
		return "", errs.NotFoundMsg("track not found")
	}
	if err != nil {
		return "", errs.Internal(fmt.Errorf("failed to resolve track release: %w", err))
	}
	return releaseID, nil
}

func (s *FileService) checkUploadResourceAuthority(
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
	spiceDB *auth.SpiceDBClient,
) (bool, error) {
	adminCan, err := policyv1.File.ManageLibrary()
	if err != nil {
		return true, errs.DependencyUnavailable("SpiceDB")
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, adminCan, spiceDB)
	if err != nil {
		return true, errs.DependencyUnavailable("SpiceDB")
	}
	if isAdmin {
		return true, nil
	}
	if target.authorOnly {
		authorCan, err := policyv1.File.List()
		if err != nil {
			return true, errs.DependencyUnavailable("SpiceDB")
		}
		isAuthor, err := checkSpiceDBCan(ctx, user, authorCan, spiceDB)
		if err != nil {
			return true, errs.DependencyUnavailable("SpiceDB")
		}
		if !isAuthor {
			return true, errs.NoPermission("upload to", "the file manager")
		}
	}
	if target.userOwned {
		if entityID != userID {
			return true, errs.PermissionDenied("cannot upload avatar for another user")
		}
		return true, nil
	}
	if target.adminOnly {
		return true, errs.AdminRequired()
	}
	return false, nil
}

func (s *FileService) checkSpiceDBUploadPermission(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	userID string,
) error {
	if !target.requiresSpiceDBCheck || target.resourceType == "" || entityID == "" {
		return nil
	}
	if _, err := uploadPermissionIdentityID(ctx, userID); err != nil {
		return err
	}
	can, err := uploadPermissionEditCan(target.resourceType, entityID)
	if err != nil {
		if errors.Is(err, errUnsupportedUploadAuthorizationResource) {
			return errs.InvalidArgument("entity_type", "unsupported authorization resource")
		}
		return errs.InvalidArgument("entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	hasPermission, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		slog.Error("SpiceDB permission check failed", "error", err, "resourceType", target.resourceType, "entityID", entityID)
		return errs.InternalMsg("permission check failed")
	}
	if !hasPermission {
		return errs.NoPermission("upload to", "this entity")
	}
	return nil
}

func (s *FileService) ensureUploadPermissionEntityExists(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
) error {
	if !target.requiresSpiceDBCheck || target.resourceType == "" || strings.TrimSpace(entityID) == "" {
		return nil
	}

	table, ok := uploadPermissionResourceTable(target.resourceType)
	if !ok {
		return nil
	}

	var count int64
	if err := s.db.WithContext(ctx).Table(table).Where("id = ?", entityID).Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count == 0 {
		return errs.NotFound(target.resourceType, entityID)
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
func (s *FileService) AbortMultipartUpload(
	ctx context.Context,
	req *connect.Request[managev1.AbortMultipartUploadRequest],
) (*connect.Response[managev1.AbortMultipartUploadResponse], error) {
	var session model.UploadSession
	if err := s.db.WithContext(ctx).
		Where("upload_id = ? AND file_id = ?", req.Msg.UploadId, req.Msg.FileId).
		First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("upload session not found")
		}
		return nil, errs.Internal(fmt.Errorf("failed to load upload session: %w", err))
	}

	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}
	entityType := ""
	if session.EntityType != nil {
		entityType = *session.EntityType
	}
	uploadType := managev1.UploadType(managev1.UploadType_value[session.UploadType])
	if err := s.checkEntityPermission(ctx, uploadType, entityType, session.EntityID, user.MemberID.String()); err != nil {
		return nil, err
	}

	progressEmitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(session.EntityType),
		session.EntityID,
		req.Msg.GetCorrelationId(),
		session.FileID,
		session.FileSize,
	)
	if progressEmitter != nil {
		if err := s.bindUploadSessionIngestEmitter(progressEmitter, session); err != nil {
			return nil, errs.Internal(err)
		}
	}
	fileKey, err := uploadSessionObjectKey(session)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("invalid upload session target: %w", err))
	}
	claimed, err := s.claimUploadSessionAbort(ctx, session.UploadID, session.FileID, time.Now())
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to claim upload session abort: %w", err))
	}
	if !claimed {
		return nil, errs.FailedPrecondition("upload session is finalizing and can no longer be aborted")
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
			return nil, errs.Internal(fmt.Errorf("failed to abort multipart upload: %w", err))
		}
		slog.Info("Multipart upload was already absent during abort cleanup", "uploadId", session.UploadID, "fileId", session.FileID)
	}

	if progressEmitter != nil {
		progressEmitter.publishFailed("Upload aborted", 0, nil)
	}

	if err := s.deleteAbortedUploadSession(cleanupCtx, session.UploadID); err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to delete upload session after abort: %w", err))
	}

	return connect.NewResponse(&managev1.AbortMultipartUploadResponse{
		Success: true,
	}), nil
}

// FindMultipartUploadCandidate returns the latest active resumable upload for an entity and upload type.
func (s *FileService) FindMultipartUploadCandidate(
	ctx context.Context,
	req *connect.Request[managev1.FindMultipartUploadCandidateRequest],
) (*connect.Response[managev1.FindMultipartUploadCandidateResponse], error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}

	search, err := s.prepareMultipartCandidateSearch(ctx, req.Msg, user.MemberID.String())
	if err != nil {
		return nil, err
	}
	session, found, err := s.findMultipartUploadCandidate(ctx, search)
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(fmt.Errorf("failed to find upload session candidate: %w", err))
	}
	if !found {
		return connect.NewResponse(&managev1.FindMultipartUploadCandidateResponse{}), nil
	}
	response, err := s.prepareMultipartCandidateResponse(ctx, session)
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(fmt.Errorf("failed to reconcile upload session candidate: %w", err))
	}
	return connect.NewResponse(response), nil
}

package filemedia

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type manageFileDeliveryAuthorization struct {
	principal *auth.UserInfo
	files     map[string]manageFileDeliveryGrant
}

type manageFileDeliveryGrant struct {
	lockFile            bool
	expectedUploader    string
	expectedAvatarOwner string
	usageWitnesses      []manageFileDeliveryUsageWitness
}

type manageFileDeliveryUsageWitness struct {
	kind             string
	fileID           string
	resourceType     string
	resourceID       string
	relationID       string
	referencePath    string
	ownerFingerprint string
}

func (w manageFileDeliveryUsageWitness) key() string {
	return w.kind + "\x00" + w.resourceType + "\x00" + w.resourceID + "\x00" + w.relationID + "\x00" + w.referencePath + "\x00" + w.fileID
}

func (s *FileService) authorizeManageFileDeliveries(ctx context.Context, fileIDs []string) error {
	_, err := s.authorizeManageFileDeliveriesWithWitness(ctx, fileIDs)
	return err
}

func (s *FileService) authorizeManageFileDeliveriesWithWitness(
	ctx context.Context,
	fileIDs []string,
) (*manageFileDeliveryAuthorization, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return nil, errs.AuthenticationRequired()
	}
	if user.Banned {
		return nil, errs.AccountBanned()
	}

	uniqueFileIDs := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, rawFileID := range fileIDs {
		fileID := strings.TrimSpace(rawFileID)
		if !IsValidUUID(fileID) {
			return nil, errs.InvalidArgument("file_id", "must be a valid UUID")
		}
		if _, ok := seen[fileID]; ok {
			continue
		}
		seen[fileID] = struct{}{}
		uniqueFileIDs = append(uniqueFileIDs, fileID)
	}
	sort.Strings(uniqueFileIDs)

	var files []model.File
	if err := s.db.WithContext(ctx).
		Select("id", "uploaded_by_member_id", "delete_requested_at").
		Where("id IN ?", uniqueFileIDs).
		Find(&files).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("load File ownership: %w", err))
	}
	fileByID := make(map[string]model.File, len(files))
	eligibleFileIDs := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			continue
		}
		eligibleFileIDs[file.ID] = struct{}{}
		fileByID[file.ID] = file
	}
	for _, fileID := range uniqueFileIDs {
		if _, ok := eligibleFileIDs[fileID]; !ok {
			return nil, errs.PermissionDenied("file access denied")
		}
	}

	listCan, err := policyv1.File.List()
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	hasLibraryAccess, err := checkSpiceDBCan(ctx, user, listCan, s.spiceDB)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	authorization := &manageFileDeliveryAuthorization{
		principal: user,
		files:     make(map[string]manageFileDeliveryGrant, len(uniqueFileIDs)),
	}
	if hasLibraryAccess {
		for _, fileID := range uniqueFileIDs {
			authorization.files[fileID] = manageFileDeliveryGrant{lockFile: true}
		}
		return authorization, nil
	}

	bindings, err := s.loadOptionalManageFileIngestBindings(ctx, uniqueFileIDs)
	if err != nil {
		return nil, err
	}
	usagesByFileID, err := s.currentManageFileDeliveryUsages(ctx, uniqueFileIDs)
	if err != nil {
		return nil, err
	}
	for _, fileID := range uniqueFileIDs {
		file := fileByID[fileID]
		ownerKind := manageFileOwnerDeliveryKind(user, file, bindings[fileID])
		if ownerKind != "" {
			grant := manageFileDeliveryGrant{lockFile: true}
			if ownerKind == "avatar" {
				grant.expectedAvatarOwner = user.MemberID.String()
			} else {
				grant.expectedUploader = user.MemberID.String()
			}
			authorization.files[fileID] = grant
			continue
		}
		usages := usagesByFileID[fileID]
		if len(usages) == 0 {
			return nil, errs.PermissionDenied("file access denied")
		}
		authorizedTargets := make(map[string]bool)
		authorizedUsages := make([]manageFileDeliveryUsageWitness, 0, len(usages))
		var denied error
		for _, usage := range usages {
			target := manageFilePermissionTarget{resourceType: usage.resourceType, resourceID: usage.resourceID}
			allowed, checked := authorizedTargets[target.key()]
			if !checked {
				err := s.authorizeManageFilePermissionTarget(ctx, user, target)
				if err == nil {
					allowed = true
				} else if connect.CodeOf(err) == connect.CodePermissionDenied || connect.CodeOf(err) == connect.CodeNotFound {
					if denied == nil {
						denied = err
					}
				} else {
					return nil, err
				}
				authorizedTargets[target.key()] = allowed
			}
			if allowed {
				authorizedUsages = append(authorizedUsages, usage)
			}
		}
		if len(authorizedUsages) == 0 {
			if denied != nil {
				return nil, denied
			}
			return nil, errs.PermissionDenied("file access denied")
		}
		authorization.files[fileID] = manageFileDeliveryGrant{usageWitnesses: authorizedUsages}
	}
	return authorization, nil
}

type currentContentFileDeliveryOwner struct {
	table               string
	resourceType        string
	hasFeaturedImageRef bool
}

var currentContentFileDeliveryOwners = []currentContentFileDeliveryOwner{
	{table: "post", resourceType: "post", hasFeaturedImageRef: true},
	{table: "page", resourceType: "page", hasFeaturedImageRef: true},
	{table: "work", resourceType: "work", hasFeaturedImageRef: true},
	{table: "program_event", resourceType: "program_event"},
}

// currentManageFileDeliveryUsages resolves exact current product references.
// Immutable ingest provenance is deliberately excluded: after detach it is no
// longer a delivery authority.
func (s *FileService) currentManageFileDeliveryUsages(
	ctx context.Context,
	fileIDs []string,
) (map[string][]manageFileDeliveryUsageWitness, error) {
	result := make(map[string][]manageFileDeliveryUsageWitness)
	if len(fileIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		FileID           string `gorm:"column:file_id"`
		ResourceType     string `gorm:"column:resource_type"`
		ResourceID       string `gorm:"column:resource_id"`
		Kind             string `gorm:"column:kind"`
		RelationID       string `gorm:"column:relation_id"`
		ReferencePath    string `gorm:"column:reference_path"`
		OwnerFingerprint string `gorm:"column:owner_fingerprint"`
	}
	queries := make([]string, 0, len(currentContentFileDeliveryOwners)*2)
	arguments := make([]any, 0, len(currentContentFileDeliveryOwners)*2)
	for _, owner := range currentContentFileDeliveryOwners {
		ownerFingerprint := `COALESCE(CAST(owner.content_document_id AS TEXT), '') || ':'`
		if owner.resourceType == "post" || owner.resourceType == "program_event" {
			ownerFingerprint = `COALESCE(CAST(owner.content_document_id AS TEXT), '') || ':' || COALESCE(CAST(owner.status AS TEXT), '')`
		}
		attachmentQuery := fmt.Sprintf(`SELECT attachment.file_id, '%s' AS resource_type, owner.id AS resource_id,
			'content_attachment' AS kind, attachment.block_id AS relation_id, attachment.reference_path,
			%s AS owner_fingerprint
			FROM content_block_attachment AS attachment
			JOIN content_block AS block ON block.id = attachment.block_id
			JOIN %s AS owner ON owner.content_document_id = block.document_id
			WHERE attachment.selector_kind = 'active' AND attachment.file_id IN ?`, owner.resourceType, ownerFingerprint, owner.table)
		queries = append(queries, attachmentQuery)
		arguments = append(arguments, fileIDs)
		if owner.hasFeaturedImageRef {
			featuredImageQuery := fmt.Sprintf(
				`SELECT owner.featured_image_file_id AS file_id, '%s' AS resource_type, owner.id AS resource_id,
				'featured_image' AS kind, owner.id AS relation_id, 'featured_image' AS reference_path,
				%s AS owner_fingerprint
				FROM %s AS owner
				WHERE owner.featured_image_file_id IN ?`,
				owner.resourceType,
				ownerFingerprint,
				owner.table,
			)
			queries = append(queries, featuredImageQuery)
			arguments = append(arguments, fileIDs)
		}
	}
	queries = append(queries,
		`SELECT relation.file_id, 'artist' AS resource_type, relation.artist_id AS resource_id,
			'artist_file' AS kind, relation.artist_id AS relation_id, '' AS reference_path, '' AS owner_fingerprint
		 FROM artist_file AS relation WHERE relation.file_id IN ?`,
		`SELECT relation.file_id, 'release' AS resource_type, relation.release_id AS resource_id,
			'release_file' AS kind, relation.release_id AS relation_id, '' AS reference_path, '' AS owner_fingerprint
		 FROM release_file AS relation WHERE relation.file_id IN ?`,
		`SELECT relation.file_id, 'program_event' AS resource_type, relation.event_id AS resource_id,
			'program_event_media' AS kind, relation.id AS relation_id, relation.role AS reference_path,
			COALESCE(CAST(owner.content_document_id AS TEXT), '') || ':' || COALESCE(CAST(owner.status AS TEXT), '') AS owner_fingerprint
		 FROM program_event_media AS relation JOIN program_event AS owner ON owner.id = relation.event_id WHERE relation.file_id IN ?`,
		`SELECT track.audio_original_file_id AS file_id, 'release' AS resource_type, track.release_id AS resource_id,
			'track' AS kind, track.id AS relation_id, 'audio_original' AS reference_path, '' AS owner_fingerprint
		 FROM track WHERE track.audio_original_file_id IN ?`,
		`SELECT label.logo_light_file_id AS file_id, 'label' AS resource_type, label.id AS resource_id,
			'label_logo' AS kind, label.id AS relation_id, 'logo_light' AS reference_path, '' AS owner_fingerprint
		 FROM label WHERE label.logo_light_file_id IN ?`,
		`SELECT label.logo_dark_file_id AS file_id, 'label' AS resource_type, label.id AS resource_id,
			'label_logo' AS kind, label.id AS relation_id, 'logo_dark' AS reference_path, '' AS owner_fingerprint
		 FROM label WHERE label.logo_dark_file_id IN ?`,
		`SELECT series.featured_image_file_id AS file_id, 'series' AS resource_type, series.id AS resource_id,
			'series_featured_image' AS kind, series.id AS relation_id, 'featured_image' AS reference_path, '' AS owner_fingerprint
		 FROM series WHERE series.featured_image_file_id IN ?`,
		`SELECT form.featured_image_file_id AS file_id, 'form' AS resource_type, form.id AS resource_id,
			'form_featured_image' AS kind, form.id AS relation_id, 'featured_image' AS reference_path, '' AS owner_fingerprint
		 FROM form WHERE form.featured_image_file_id IN ?`)
	for range 8 {
		arguments = append(arguments, fileIDs)
	}
	query := `SELECT DISTINCT current_content_files.file_id,
			current_content_files.resource_type,
			current_content_files.resource_id,
			current_content_files.kind,
			current_content_files.relation_id,
			current_content_files.reference_path,
			current_content_files.owner_fingerprint
		FROM (` + strings.Join(queries, ` UNION ALL `) + `) AS current_content_files`
	if err := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("load current Content file usage: %w", err))
	}
	seen := make(map[string]map[string]struct{})
	for _, row := range rows {
		resourceType, ok := currentManageFileDeliveryResourceType(row.ResourceType)
		if row.FileID == "" || row.ResourceID == "" || !ok {
			continue
		}
		if seen[row.FileID] == nil {
			seen[row.FileID] = make(map[string]struct{})
		}
		witness := manageFileDeliveryUsageWitness{
			kind: row.Kind, fileID: row.FileID, resourceType: resourceType, resourceID: row.ResourceID,
			relationID: row.RelationID, referencePath: row.ReferencePath, ownerFingerprint: row.OwnerFingerprint,
		}
		if _, exists := seen[row.FileID][witness.key()]; exists {
			continue
		}
		seen[row.FileID][witness.key()] = struct{}{}
		result[row.FileID] = append(result[row.FileID], witness)
	}
	for fileID := range result {
		sort.Slice(result[fileID], func(i, j int) bool {
			return result[fileID][i].key() < result[fileID][j].key()
		})
	}
	return result, nil
}

func currentManageFileDeliveryResourceType(value string) (string, bool) {
	switch value {
	case "artist", "form", "label", "page", "post", "program_event", "release", "series", "work":
		return value, true
	default:
		return "", false
	}
}

func (s *FileService) loadOptionalManageFileIngestBindings(
	ctx context.Context,
	fileIDs []string,
) (map[string]model.FileIngestBinding, error) {
	result := make(map[string]model.FileIngestBinding, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	var bindings []model.FileIngestBinding
	if err := s.db.WithContext(ctx).Where("file_id IN ?", fileIDs).Find(&bindings).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("load file ingest classification: %w", err))
	}
	for _, binding := range bindings {
		result[binding.FileID] = binding
	}
	return result, nil
}

func manageFileOwnerDeliveryKind(user *auth.UserInfo, file model.File, binding model.FileIngestBinding) string {
	if user == nil {
		return ""
	}
	uploadTypeValue, ok := managev1.UploadType_value[binding.UploadType]
	if !ok {
		return ""
	}
	uploadType := managev1.UploadType(uploadTypeValue)
	if uploadType == managev1.UploadType_UPLOAD_TYPE_USER_AVATAR {
		if strings.TrimSpace(binding.EntityID) == user.MemberID.String() {
			return "avatar"
		}
		return ""
	}
	if file.UploadedByMemberID == nil || strings.TrimSpace(*file.UploadedByMemberID) != user.MemberID.String() {
		return ""
	}
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH:
		return "uploader"
	default:
		return ""
	}
}

type manageFilePermissionTarget struct {
	resourceType string
	resourceID   string
}

func (target manageFilePermissionTarget) key() string {
	return target.resourceType + "\x00" + target.resourceID
}

func (s *FileService) authorizeManageFilePermissionTarget(
	ctx context.Context,
	user *auth.UserInfo,
	target manageFilePermissionTarget,
) error {
	if target.resourceType == "post" {
		access, err := requirePostAccess(s.postAccess)
		if err != nil {
			return err
		}
		return access.RequireView(ctx, target.resourceID)
	}
	if target.resourceType == "program_event" {
		access, err := requireProgramEventAttachment(s.programEventAttachment)
		if err != nil {
			return err
		}
		return access.RequireView(ctx, s.spiceDB, target.resourceID)
	}
	adminCan, err := policyv1.File.ManageLibrary()
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, adminCan, s.spiceDB)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if isAdmin {
		return nil
	}
	can, err := uploadPermissionEditCan(target.resourceType, target.resourceID)
	if errors.Is(err, errUnsupportedUploadAuthorizationResource) {
		return errs.PermissionDenied("file access denied")
	}
	if err != nil {
		return errs.PermissionDenied("file access denied")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.InternalMsg("permission check failed")
	}
	if !allowed {
		return errs.PermissionDenied("file access denied")
	}
	return nil
}

package filemedia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type scopedContentOwner string

const (
	scopedContentOwnerPage         scopedContentOwner = "page"
	scopedContentOwnerPost         scopedContentOwner = "post"
	scopedContentOwnerProgramEvent scopedContentOwner = "program_event"
	scopedContentOwnerWork         scopedContentOwner = "work"
)

type scopedContentAttachmentRow struct {
	BlockID       string `gorm:"column:block_id"`
	ReferencePath string `gorm:"column:reference_path"`
	FileID        string `gorm:"column:file_id"`
}

func (row scopedContentAttachmentRow) key() string {
	return row.BlockID + "\x00" + row.ReferencePath + "\x00" + row.FileID
}

// ResolveAuthorizedPostFeaturedImage is the final private-inline issuance
// boundary for one Post featured-image slot. The Post owner locks lifecycle
// and the active principal in the same transaction used to re-read the slot,
// lock the exact File, and sign the response.
func (s *FileService) ResolveAuthorizedPostFeaturedImage(
	ctx context.Context,
	postID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	access, err := requirePostAccess(s.postAccess)
	if err != nil {
		return nil, err
	}
	return s.resolveAuthorizedFeaturedImage(
		ctx,
		scopedContentOwnerPost,
		postID,
		expectedFileID,
		func(tx *gorm.DB) error { return access.RequireLockedView(ctx, tx, postID) },
	)
}

// ResolveAuthorizedPageFeaturedImage is the final private-inline issuance
// boundary for one Page featured-image slot.
func (s *FileService) ResolveAuthorizedPageFeaturedImage(
	ctx context.Context,
	pageID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	access, err := requirePagePolicyAccess(s.pagePolicyAccess)
	if err != nil {
		return nil, err
	}
	return s.resolveAuthorizedFeaturedImage(
		ctx,
		scopedContentOwnerPage,
		pageID,
		expectedFileID,
		func(tx *gorm.DB) error { return access.RequireLockedView(ctx, tx, pageID) },
	)
}

func (s *FileService) resolveAuthorizedFeaturedImage(
	ctx context.Context,
	owner scopedContentOwner,
	ownerID string,
	expectedFileID string,
	authorize func(*gorm.DB) error,
) (*commonv1.MediaDelivery, error) {
	if s == nil || s.db == nil {
		return nil, errs.DependencyUnavailable("File delivery database")
	}
	if _, err := uuidutil.ParseCanonical(strings.TrimSpace(ownerID), "owner_id"); err != nil {
		return nil, errs.InvalidArgument("owner_id", "must be a canonical UUID")
	}
	expectedFileID = strings.TrimSpace(expectedFileID)
	if expectedFileID != "" {
		if _, err := uuidutil.ParseCanonical(expectedFileID, "expected_file_id"); err != nil {
			return nil, errs.InvalidArgument("expected_file_id", "must be a canonical UUID")
		}
	}
	if authorize == nil {
		return nil, errs.InternalMsg("featured image authorization is required")
	}

	var delivery *commonv1.MediaDelivery
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := authorize(tx); err != nil {
			return err
		}
		var slot struct {
			FileID *string `gorm:"column:featured_image_file_id"`
		}
		if err := tx.WithContext(ctx).
			Table(string(owner)).
			Select("featured_image_file_id").
			Where("id = ?", ownerID).
			Take(&slot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound(string(owner), ownerID)
			}
			return errs.Internal(err)
		}
		currentFileID := ""
		if slot.FileID != nil {
			currentFileID = strings.TrimSpace(*slot.FileID)
		}
		if currentFileID != expectedFileID || currentFileID == "" {
			return nil
		}

		files, err := lockScopedDeliveryFiles(ctx, tx, []string{currentFileID})
		if err != nil {
			return err
		}
		file, ok := files[currentFileID]
		if !ok || file.DeleteRequestedAt != nil {
			return nil
		}
		delivery, err = s.buildLockedInlineDelivery(ctx, tx, file)
		if err == nil && s.testAfterScopedFeaturedSigned != nil {
			s.testAfterScopedFeaturedSigned(string(owner), ownerID, currentFileID)
		}
		return err
	})
	return delivery, err
}

// HydrateAuthorizedPageBlockMediaWithDB adds private editor delivery while
// the Page owner keeps its root, principal, document, File, and exact
// attachment witnesses in one transaction.
func (s *FileService) HydrateAuthorizedPageBlockMediaWithDB(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMediaWithDB(
		ctx, tx, scopedContentOwnerPage, pageID, documentID, principal, items,
	)
}

// HydrateAuthorizedWorkBlockMediaWithDB adds private editor delivery while
// the Work owner keeps its exact authorization and lifecycle fence.
func (s *FileService) HydrateAuthorizedWorkBlockMediaWithDB(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMediaWithDB(
		ctx, tx, scopedContentOwnerWork, workID, documentID, principal, items,
	)
}

// HydrateAuthorizedProgramEventBlockMediaWithDB adds private editor delivery
// while Program Event keeps its exact authorization and lifecycle fence.
func (s *FileService) HydrateAuthorizedProgramEventBlockMediaWithDB(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMediaWithDB(
		ctx, tx, scopedContentOwnerProgramEvent, eventID, documentID, principal, items,
	)
}

func (s *FileService) hydrateAuthorizedContentBlockMediaWithDB(
	ctx context.Context,
	tx *gorm.DB,
	owner scopedContentOwner,
	ownerID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	if s == nil || tx == nil {
		return nil, errs.InternalMsg("scoped content media transaction is required")
	}
	if _, err := uuidutil.ParseCanonical(strings.TrimSpace(ownerID), "owner_id"); err != nil {
		return nil, errs.InvalidArgument("owner_id", "must be a canonical UUID")
	}
	if documentID == uuid.Nil || principal == nil || !principal.Authenticated {
		return nil, errs.AuthenticationRequired()
	}

	cloned := make([]*contentv1.ContentBlockMediaItem, 0, len(items))
	requestedFileIDs := make(map[string]struct{}, len(items))
	requestedBlockIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		copy := proto.Clone(item).(*contentv1.ContentBlockMediaItem)
		copy.Delivery = nil
		copy.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_UNAVAILABLE
		copy.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE
		cloned = append(cloned, copy)
		fileID := strings.TrimSpace(copy.GetAttachment().GetActiveFileId())
		blockID := strings.TrimSpace(copy.GetSelector().GetBlockId())
		if fileID == "" || blockID == "" || !IsValidUUID(fileID) || !IsValidUUID(blockID) {
			continue
		}
		requestedFileIDs[fileID] = struct{}{}
		requestedBlockIDs[blockID] = struct{}{}
	}

	scopedCtx := auth.WithUser(ctx, principal)
	hasLibraryDownload := false
	if can, err := policyv1.File.List(); err == nil {
		if allowed, checkErr := checkSpiceDBCan(scopedCtx, principal, can, s.spiceDB); checkErr == nil {
			hasLibraryDownload = allowed
		}
	}

	fileIDs := sortedStringSet(requestedFileIDs)
	files, err := lockScopedDeliveryFiles(scopedCtx, tx, fileIDs)
	if err != nil {
		return nil, err
	}
	relations, err := lockScopedContentAttachments(
		scopedCtx,
		tx,
		owner,
		ownerID,
		documentID,
		sortedStringSet(requestedBlockIDs),
	)
	if err != nil {
		return nil, err
	}

	currentFileIDs := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		if file, ok := files[relation.FileID]; ok && file.DeleteRequestedAt == nil {
			currentFileIDs[relation.FileID] = struct{}{}
		}
	}
	scopedService := *s
	scopedService.db = tx
	responses, err := scopedService.loadFileURLResponses(scopedCtx, sortedStringSet(currentFileIDs))
	if err != nil {
		return nil, err
	}

	for _, item := range cloned {
		fileID := strings.TrimSpace(item.GetAttachment().GetActiveFileId())
		key := strings.TrimSpace(item.GetSelector().GetBlockId()) + "\x00" +
			strings.TrimSpace(item.GetSelector().GetReferencePath()) + "\x00" + fileID
		if _, ok := relations[key]; !ok {
			continue
		}
		file, ok := files[fileID]
		if !ok || file.DeleteRequestedAt != nil {
			continue
		}
		response := responses[fileID]
		if response == nil || response.GetDelivery() == nil {
			continue
		}
		delivery := proto.Clone(response.GetDelivery()).(*commonv1.MediaDelivery)
		allowDownload := hasLibraryDownload ||
			(file.UploadedByMemberID != nil && strings.TrimSpace(*file.UploadedByMemberID) == principal.MemberID.String())
		if !allowDownload {
			delivery.Download = nil
		} else {
			item.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE
			item.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD
		}
		item.Delivery = delivery
	}
	if s.testAfterScopedBlockMediaSigned != nil {
		s.testAfterScopedBlockMediaSigned(string(owner), ownerID, sortedStringSet(currentFileIDs))
	}
	return cloned, nil
}

func lockScopedDeliveryFiles(
	ctx context.Context,
	tx *gorm.DB,
	fileIDs []string,
) (map[string]model.File, error) {
	result := make(map[string]model.File, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	var files []model.File
	query := tx.WithContext(ctx).
		Table("file").
		Select("id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "uploaded_by_member_id", "delete_requested_at").
		Where("id IN ?", fileIDs).
		Order("id ASC")
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	if err := query.Find(&files).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("lock scoped delivery Files: %w", err))
	}
	for _, file := range files {
		result[file.ID] = file
	}
	return result, nil
}

func lockScopedContentAttachments(
	ctx context.Context,
	tx *gorm.DB,
	owner scopedContentOwner,
	ownerID string,
	documentID uuid.UUID,
	blockIDs []string,
) (map[string]scopedContentAttachmentRow, error) {
	result := make(map[string]scopedContentAttachmentRow)
	if len(blockIDs) == 0 {
		return result, nil
	}
	ownerTable := string(owner)
	switch owner {
	case scopedContentOwnerPage, scopedContentOwnerPost, scopedContentOwnerProgramEvent, scopedContentOwnerWork:
	default:
		return nil, errs.InternalMsg("unsupported scoped content owner")
	}
	var rows []scopedContentAttachmentRow
	query := tx.WithContext(ctx).
		Table("content_block_attachment AS attachment").
		Select("attachment.block_id::text AS block_id", "attachment.reference_path", "attachment.file_id::text AS file_id").
		Joins("JOIN content_block AS block ON block.id = attachment.block_id").
		Joins("JOIN "+ownerTable+" AS owner ON owner.content_document_id = block.document_id").
		Where("owner.id = ? AND owner.content_document_id = ? AND attachment.selector_kind = 'active' AND attachment.block_id IN ?", ownerID, documentID, blockIDs).
		Order("attachment.block_id ASC, attachment.reference_path ASC")
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "SHARE", Table: clause.Table{Name: "attachment"}})
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("lock exact scoped content attachments: %w", err))
	}
	for _, row := range rows {
		result[row.key()] = row
	}
	return result, nil
}

func (s *FileService) buildLockedInlineDelivery(
	ctx context.Context,
	tx *gorm.DB,
	file model.File,
) (*commonv1.MediaDelivery, error) {
	inline, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		file.ID,
		file.Extension,
		file.MimeType,
		nil,
		mediaauth.PurposeInline,
		mediaauth.InlineTTL,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	delivery := &commonv1.MediaDelivery{
		FileId:    file.ID,
		Extension: file.Extension,
		MimeType:  file.MimeType,
		FileSize:  file.FileSize,
		FileName:  optionalNonEmptyString(file.FileName),
		Inline:    inline,
	}
	if file.DurationSeconds != nil {
		duration := int32(*file.DurationSeconds)
		delivery.DurationSeconds = &duration
	}
	scopedService := *s
	scopedService.db = tx
	if err := scopedService.populateFileProcessingStatus(ctx, map[string]*commonv1.MediaDelivery{file.ID: delivery}); err != nil {
		slog.Warn("Failed to populate scoped featured-image processing status", "error", err, "fileId", file.ID)
	}
	return delivery, nil
}

func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

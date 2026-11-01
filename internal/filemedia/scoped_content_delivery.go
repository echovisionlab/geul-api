package filemedia

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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

// ResolveAuthorizedPostFeaturedImage is the final private-inline issuance
// boundary for one Post featured-image slot. The Post owner locks lifecycle
// and the active principal in the same transaction used to re-read the slot,
// lock the exact File, and sign the response.
func (s *Service) ResolveAuthorizedPostFeaturedImage(
	ctx context.Context,
	postID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	return s.resolveAuthorizedFeaturedImage(
		ctx,
		scopedContentOwnerPost,
		postID,
		expectedFileID,
	)
}

// ResolveAuthorizedPageFeaturedImage is the final private-inline issuance
// boundary for one Page featured-image slot.
func (s *Service) ResolveAuthorizedPageFeaturedImage(
	ctx context.Context,
	pageID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	return s.resolveAuthorizedFeaturedImage(
		ctx,
		scopedContentOwnerPage,
		pageID,
		expectedFileID,
	)
}

func (s *Service) resolveAuthorizedFeaturedImage(
	ctx context.Context,
	owner scopedContentOwner,
	ownerID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	if _, err := uuidutil.ParseCanonical(strings.TrimSpace(ownerID), "owner_id"); err != nil {
		return nil, domainerr.InvalidField("owner_id", "must be a canonical UUID")
	}
	expectedFileID = strings.TrimSpace(expectedFileID)
	if expectedFileID != "" {
		if _, err := uuidutil.ParseCanonical(expectedFileID, "expected_file_id"); err != nil {
			return nil, domainerr.InvalidField("expected_file_id", "must be a canonical UUID")
		}
	}

	var delivery *commonv1.MediaDelivery
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		if err := store.RequirePolicyOwner(ctx, string(owner), ownerID, false); err != nil {
			return err
		}
		slotID, found, err := store.FeaturedFileID(ctx, string(owner), ownerID)
		if err != nil {
			return err
		}
		if !found {
			return domainerr.ResourceNotFound(string(owner), ownerID)
		}
		currentFileID := ""
		if slotID != nil {
			currentFileID = strings.TrimSpace(*slotID)
		}
		if currentFileID != expectedFileID || currentFileID == "" {
			return nil
		}

		files, err := store.LockScopedFiles(ctx, []string{currentFileID})
		if err != nil {
			return err
		}
		file, ok := files[currentFileID]
		if !ok || file.DeleteRequestedAt != nil {
			return nil
		}
		delivery, err = s.buildLockedInlineDelivery(ctx, store, file)
		if err == nil && s.testAfterScopedFeaturedSigned != nil {
			s.testAfterScopedFeaturedSigned(string(owner), ownerID, currentFileID)
		}
		return err
	})
	return delivery, err
}

// HydrateAuthorizedPageBlockMedia adds private editor delivery while
// the Page owner keeps its root, principal, document, File, and exact
// attachment witnesses in one transaction.
func (s *Service) HydrateAuthorizedPageBlockMedia(
	ctx context.Context,
	joined Joined,
	pageID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMedia(
		ctx, joined, scopedContentOwnerPage, pageID, documentID, principal, items,
	)
}

// HydrateAuthorizedPostBlockMedia adds private editor delivery while
// Post holds its owner, principal, document, and exact attachment witnesses.
func (s *Service) HydrateAuthorizedPostBlockMedia(
	ctx context.Context, joined Joined, postID string, documentID uuid.UUID,
	principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMedia(ctx, joined, scopedContentOwnerPost, postID, documentID, principal, items)
}

// HydrateAuthorizedWorkBlockMedia adds private editor delivery while
// the Work owner keeps its exact authorization and lifecycle fence.
func (s *Service) HydrateAuthorizedWorkBlockMedia(
	ctx context.Context,
	joined Joined,
	workID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMedia(
		ctx, joined, scopedContentOwnerWork, workID, documentID, principal, items,
	)
}

// HydrateAuthorizedProgramEventBlockMedia adds private editor delivery
// while Program Event keeps its exact authorization and lifecycle fence.
func (s *Service) HydrateAuthorizedProgramEventBlockMedia(
	ctx context.Context,
	joined Joined,
	eventID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMedia(
		ctx, joined, scopedContentOwnerProgramEvent, eventID, documentID, principal, items,
	)
}

func (s *Service) hydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	joined Joined,
	owner scopedContentOwner,
	ownerID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	if s == nil || joined.Store == nil {
		return nil, errors.New("scoped content media transaction is required")
	}
	if _, err := uuidutil.ParseCanonical(strings.TrimSpace(ownerID), "owner_id"); err != nil {
		return nil, domainerr.InvalidField("owner_id", "must be a canonical UUID")
	}
	if documentID == uuid.Nil || principal == nil || !principal.Authenticated {
		return nil, domainerr.AuthenticationRequired()
	}
	cloned, requestedFileIDs, requestedBlockIDs := undeliveredContentBlockMediaItems(items)
	scopedCtx := auth.WithUser(ctx, principal)
	hasLibraryDownload := s.hasScopedLibraryDownload(scopedCtx, principal)

	files, err := joined.Store.LockScopedFiles(scopedCtx, sortedStringSet(requestedFileIDs))
	if err != nil {
		return nil, err
	}
	relations, err := joined.Store.LockScopedAttachments(scopedCtx, string(owner), ownerID, documentID.String(), sortedStringSet(requestedBlockIDs))
	if err != nil {
		return nil, err
	}
	currentFileIDs := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		if file, ok := files[relation.FileID]; ok && file.DeleteRequestedAt == nil {
			currentFileIDs[relation.FileID] = struct{}{}
		}
	}
	responses, err := s.loadFileURLResponsesWithReader(scopedCtx, boundFileStoreReader{joined.Store}, sortedStringSet(currentFileIDs))
	if err != nil {
		return nil, err
	}
	for _, item := range cloned {
		deliverScopedContentBlockMediaItem(item, relations, files, responses, principal, hasLibraryDownload)
	}
	if s.testAfterScopedBlockMediaSigned != nil {
		s.testAfterScopedBlockMediaSigned(string(owner), ownerID, sortedStringSet(currentFileIDs))
	}
	return cloned, nil
}

// undeliveredContentBlockMediaItems clones the items without delivery or
// download action and collects the File and Block IDs they reference.
func undeliveredContentBlockMediaItems(items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, map[string]struct{}, map[string]struct{}) {
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
	return cloned, requestedFileIDs, requestedBlockIDs
}

// hasScopedLibraryDownload reports whether the principal may download any
// File of the library. A failed check grants nothing and is logged.
func (s *Service) hasScopedLibraryDownload(ctx context.Context, principal *auth.UserInfo) bool {
	can, err := policyv1.File.List()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build scoped file library policy", failurecause.Attr(err))
		return false
	}
	allowed, err := checkSpiceDBCan(ctx, principal, can, s.spiceDB)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to check scoped file library access", failurecause.Attr(err))
		return false
	}
	return allowed
}

// deliverScopedContentBlockMediaItem gives an item whose locked attachment
// still references a live File that File's delivery, with the download when
// the principal may download it.
func deliverScopedContentBlockMediaItem(
	item *contentv1.ContentBlockMediaItem,
	relations map[string]domain.ScopedAttachment,
	files map[string]File,
	responses map[string]*managev1.GetMediaDeliveryResponse,
	principal *auth.UserInfo,
	hasLibraryDownload bool,
) {
	fileID := strings.TrimSpace(item.GetAttachment().GetActiveFileId())
	key := strings.TrimSpace(item.GetSelector().GetBlockId()) + "\x00" +
		strings.TrimSpace(item.GetSelector().GetReferencePath()) + "\x00" + fileID
	if _, ok := relations[key]; !ok {
		return
	}
	file, ok := files[fileID]
	if !ok || file.DeleteRequestedAt != nil {
		return
	}
	response := responses[fileID]
	if response == nil || response.GetDelivery() == nil {
		return
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

func (s *Service) buildLockedInlineDelivery(
	ctx context.Context,
	store Store,
	file File,
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
		return nil, err
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
	if err := s.populateFileProcessingStatusWithReader(ctx, boundFileStoreReader{store}, map[string]*commonv1.MediaDelivery{file.ID: delivery}); err != nil {
		slog.ErrorContext(ctx, "Failed to populate scoped featured-image processing status", failurecause.Attr(err), "fileId", file.ID)
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

package public

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// hydrateAuthorizedContentBlockMedia adds request-scoped delivery and download
// actions to references loaded in the owning domain's document snapshot. This
// relation lock is held through download signing so replacement, removal, or a
// policy reset cannot commit and then receive a newly issued URL for stale data.
func (s *FileService) hydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	if len(items) == 0 {
		return items, nil
	}

	selectors := make([]mediaasset.ContentBlockDownloadSelector, 0, len(items))
	for _, item := range items {
		fileID := strings.TrimSpace(item.GetAttachment().GetActiveFileId())
		selector := item.GetSelector()
		if fileID == "" || selector == nil {
			continue
		}
		selectors = append(selectors, mediaasset.ContentBlockDownloadSelector{
			BlockID: selector.GetBlockId(), ReferencePath: selector.GetReferencePath(),
		})
	}

	var err error
	user := auth.GetUser(ctx)
	var evaluatedSources map[string]mediaasset.FileDownloadSource
	var evaluatedFingerprints map[string]string
	var evaluatedViewerFingerprint string
	var evaluatedOwners map[string]contentBlockDownloadOwnerWitness
	var evaluatedRelations map[string]string
	ownerAuthorization, ownerAuthorizationPresent := mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	evaluatedOwnerAuthorized := false
	evaluatedDecisions := make(map[string]*openv1.FileDownloadAccess, len(selectors))
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		owners, ownerErr := loadContentBlockDownloadOwnerWitnesses(ctx, tx, selectors)
		if ownerErr != nil {
			return errs.Internal(ownerErr)
		}
		ownerAuthorized, authorizationErr := s.validateContentDownloadOwnerAuthorization(
			ctx, tx, ownerAuthorization, ownerAuthorizationPresent, owners, false,
		)
		if authorizationErr != nil {
			return authorizationErr
		}
		relations, relationErr := loadContentBlockMediaRelations(ctx, tx, selectors, false)
		if relationErr != nil {
			return errs.Internal(relationErr)
		}
		sources, loadErr := mediaasset.LoadContentBlockDownloadSources(ctx, tx, selectors)
		if loadErr != nil {
			return errs.Internal(loadErr)
		}
		allowed, evaluateErr := mediaasset.EvaluateFileDownloadAccessBatch(ctx, tx, s.spiceDB, sources, user, s.segments)
		if evaluateErr != nil {
			return errs.Internal(evaluateErr)
		}
		signIn := make(map[string]bool, len(sources))
		if user == nil || !user.Authenticated || user.MemberID == "" {
			restricted := make(map[string]mediaasset.FileDownloadSource)
			for key, source := range sources {
				switch source.Audience {
				case mediaasset.FileDownloadAudienceAuthenticated:
					signIn[key] = true
				case mediaasset.FileDownloadAudienceRestricted:
					restricted[key] = source
				}
			}
			presence, presenceErr := mediaasset.RestrictedFileDownloadSegmentPresence(ctx, tx, restricted)
			if presenceErr != nil {
				return errs.Internal(presenceErr)
			}
			for key, present := range presence {
				signIn[key] = present
			}
		}
		fingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, sources, s.segments)
		if fingerprintErr != nil {
			return errs.Internal(fingerprintErr)
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, sources, user)
		if viewerErr != nil {
			return errs.Internal(viewerErr)
		}
		for key, source := range sources {
			if ownerAuthorized {
				evaluatedDecisions[key] = effectiveFileDownloadAccess(source.Audience, user, allowed[key], signIn[key])
			} else {
				evaluatedDecisions[key] = unavailableFileDownloadAccess()
			}
		}
		evaluatedSources = sources
		evaluatedFingerprints = fingerprints
		evaluatedViewerFingerprint = viewerFingerprint
		evaluatedOwners = owners
		evaluatedRelations = relations
		evaluatedOwnerAuthorized = ownerAuthorized
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lockErr := mediaasset.LockFileDownloadPolicySegments(ctx, tx, evaluatedSources); lockErr != nil {
			return errs.Internal(lockErr)
		}
		currentOwners, ownerErr := lockContentBlockDownloadOwners(ctx, tx, evaluatedOwners)
		if ownerErr != nil {
			return errs.Internal(ownerErr)
		}
		currentOwnerAuthorized, authorizationErr := s.validateContentDownloadOwnerAuthorization(
			ctx, tx, ownerAuthorization, ownerAuthorizationPresent, currentOwners, true,
		)
		if authorizationErr != nil {
			return authorizationErr
		}
		viewerActive, lockErr := mediaasset.LockFileDownloadViewerFacts(ctx, tx, evaluatedSources, user)
		if lockErr != nil {
			return errs.Internal(lockErr)
		}
		currentRelations, relationErr := loadContentBlockMediaRelations(ctx, tx, selectors, true)
		if relationErr != nil {
			return errs.Internal(relationErr)
		}
		currentSources, loadErr := mediaasset.LoadContentBlockDownloadSources(ctx, tx, selectors)
		if loadErr != nil {
			return errs.Internal(loadErr)
		}
		currentFingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, currentSources, s.segments)
		if fingerprintErr != nil {
			return errs.Internal(fingerprintErr)
		}
		currentViewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, currentSources, user)
		if viewerErr != nil {
			return errs.Internal(viewerErr)
		}
		decisions := make(map[string]*openv1.FileDownloadAccess, len(items))
		deliveryAccess := make(map[string]resolvedMediaAccess, len(items))
		stableRelations := make(map[string]struct{}, len(items))
		for _, item := range items {
			fileID := strings.TrimSpace(item.GetAttachment().GetActiveFileId())
			selector := item.GetSelector()
			if fileID == "" || selector == nil {
				continue
			}
			key := mediaasset.ContentBlockDownloadPolicyKey(selector.GetBlockId(), selector.GetReferencePath())
			evaluatedOwner, evaluatedOwnerExists := evaluatedOwners[selector.GetBlockId()]
			currentOwner, currentOwnerExists := currentOwners[selector.GetBlockId()]
			decision := unavailableFileDownloadAccess()
			evaluatedRelationFileID, evaluatedRelationExists := evaluatedRelations[key]
			currentRelationFileID, currentRelationExists := currentRelations[key]
			relationStable := evaluatedOwnerAuthorized && currentOwnerAuthorized &&
				evaluatedRelationExists && currentRelationExists &&
				evaluatedOwnerExists && currentOwnerExists && evaluatedOwner == currentOwner &&
				evaluatedRelationFileID == fileID && currentRelationFileID == fileID
			if relationStable {
				if source, exists := currentSources[key]; exists {
					if evaluated, evaluatedExists := evaluatedSources[key]; evaluatedExists &&
						source.FileID == fileID && evaluated.FileID == fileID &&
						currentFingerprints[key] == evaluatedFingerprints[key] {
						if !fileDownloadDecisionDependsOnActiveViewer(evaluated, evaluatedDecisions[key]) ||
							(viewerActive && currentViewerFingerprint == evaluatedViewerFingerprint) {
							decision = evaluatedDecisions[key]
						}
					}
				}
			}
			decisions[key] = decision
			if relationStable {
				stableRelations[key] = struct{}{}
				deliveryAccess[fileID] = resolvedMediaAccess{}
			}
		}
		deliveries, deliveryErr := s.buildScopedMediaURLsWithDB(ctx, tx, deliveryAccess)
		if deliveryErr != nil {
			return deliveryErr
		}
		for _, item := range items {
			fileID := strings.TrimSpace(item.GetAttachment().GetActiveFileId())
			selector := item.GetSelector()
			if fileID == "" || selector == nil {
				continue
			}
			key := mediaasset.ContentBlockDownloadPolicyKey(selector.GetBlockId(), selector.GetReferencePath())
			decision := decisions[key]
			if decision == nil {
				decision = unavailableFileDownloadAccess()
			}
			if _, stable := stableRelations[key]; !stable {
				applyContentBlockDownloadDecision(item, decision)
				continue
			}
			if delivery := deliveries[fileID]; delivery != nil {
				item.Delivery = proto.Clone(delivery).(*commonv1.MediaDelivery)
				if decision.GetAction() == openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD {
					source := currentSources[key]
					download, downloadErr := buildExpiringFileRef(
						s.mediaDomain, s.mediaSecret, source.FileID, source.Extension,
						source.MimeType, source.FileName, mediaauth.PurposeDownload, s.effectiveDownloadTTL(),
					)
					if downloadErr != nil {
						return errs.Internal(downloadErr)
					}
					item.Delivery.Download = download
				}
			}
			applyContentBlockDownloadDecision(item, decision)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *FileService) validateContentDownloadOwnerAuthorization(
	ctx context.Context,
	db *gorm.DB,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
	present bool,
	owners map[string]contentBlockDownloadOwnerWitness,
	locked bool,
) (bool, error) {
	if !present || len(owners) == 0 {
		return false, nil
	}
	for _, owner := range owners {
		if authorization.ResourceType != owner.ResourceType || authorization.ResourceID != owner.ResourceID ||
			authorization.Status != owner.Status || authorization.DocumentID != owner.DocumentID {
			return false, nil
		}
	}
	switch authorization.Mode {
	case mediaasset.ContentDownloadOwnerAccessPublic:
		return contentDownloadOwnerStatusIsPublic(authorization.ResourceType, authorization.Status), nil
	case mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft:
		user := auth.GetUser(ctx)
		if user == nil || !user.Authenticated || user.Banned ||
			user.IdentityID.String() != authorization.IdentityID || user.MemberID.String() != authorization.MemberID {
			return false, nil
		}
		if locked {
			return lockContentDownloadOwnerPrincipal(ctx, db, authorization)
		}
		var action auth.ResourceAction
		switch authorization.ResourceType {
		case "post":
			action = policyv1.Post.View
		case "page":
			action = policyv1.Page.View
		case "work":
			action = policyv1.Work.View
		case "program_event":
			action = policyv1.ProgramEvent.View
		default:
			return false, nil
		}
		return hasDraftResourceView(ctx, s.spiceDB, action, authorization.ResourceID)
	case mediaasset.ContentDownloadOwnerAccessShare:
		return validateContentDownloadShareLinkWitness(ctx, db, authorization, locked)
	default:
		return false, nil
	}
}

func contentDownloadOwnerStatusIsPublic(resourceType, status string) bool {
	switch resourceType {
	case "post":
		return status == managev1.PostStatus_POST_STATUS_PUBLISHED.String() || status == managev1.PostStatus_POST_STATUS_ARCHIVED.String()
	case "page":
		return status == managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()
	case "work":
		return status == managev1.WorkStatus_WORK_STATUS_PUBLISHED.String() || status == managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()
	case "program_event":
		return status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String() || status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()
	default:
		return false
	}
}

func lockContentDownloadOwnerPrincipal(
	ctx context.Context,
	db *gorm.DB,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
) (bool, error) {
	principal := &auth.UserInfo{
		IdentityID:    auth.IdentityID(authorization.IdentityID),
		MemberID:      auth.MemberID(authorization.MemberID),
		Authenticated: true, Onboarded: true,
	}
	return identitystate.LockActivePrincipal(ctx, db, principal)
}

func validateContentDownloadShareLinkWitness(
	ctx context.Context,
	db *gorm.DB,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
	locked bool,
) (bool, error) {
	witness := authorization.ShareLink
	expectedEntityType := contentDownloadShareLinkEntityType(authorization.ResourceType)
	if witness == nil || witness.ID == "" || expectedEntityType == "" || witness.EntityType != expectedEntityType || witness.EntityID != authorization.ResourceID {
		return false, nil
	}
	var link model.ShareLink
	query := db.WithContext(ctx).Where("id = ?", witness.ID)
	if locked {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	if err := query.Take(&link).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	if link.ID != witness.ID || link.EntityType != witness.EntityType || link.EntityID != witness.EntityID ||
		!sameContentDownloadTime(link.ExpiresAt, witness.ExpiresAt) {
		return false, nil
	}
	return link.ExpiresAt != nil && link.ExpiresAt.After(time.Now()), nil
}

func contentDownloadShareLinkEntityType(resourceType string) string {
	switch resourceType {
	case "post":
		return managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST.String()
	case "page":
		return managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String()
	case "work":
		return managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK.String()
	case "release":
		return managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String()
	default:
		return ""
	}
}

func sameContentDownloadTime(left, right *time.Time) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && left.Equal(*right))
}

type contentBlockDownloadOwnerWitness struct {
	ResourceType string `gorm:"column:resource_type"`
	ResourceID   string `gorm:"column:resource_id"`
	Status       string `gorm:"column:status"`
	DocumentID   string `gorm:"column:document_id"`
}

func loadContentBlockDownloadOwnerWitnesses(
	ctx context.Context,
	db *gorm.DB,
	selectors []mediaasset.ContentBlockDownloadSelector,
) (map[string]contentBlockDownloadOwnerWitness, error) {
	blockIDs := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if blockID := strings.TrimSpace(selector.BlockID); blockID != "" {
			blockIDs = append(blockIDs, blockID)
		}
	}
	if len(blockIDs) == 0 {
		return map[string]contentBlockDownloadOwnerWitness{}, nil
	}
	type row struct {
		BlockID      string `gorm:"column:block_id"`
		ResourceType string `gorm:"column:resource_type"`
		ResourceID   string `gorm:"column:resource_id"`
		Status       string `gorm:"column:status"`
		DocumentID   string `gorm:"column:document_id"`
	}
	var rows []row
	if err := db.WithContext(ctx).Raw(`
		SELECT block.id AS block_id, 'post' AS resource_type, owner.id AS resource_id, CAST(owner.status AS TEXT) AS status, owner.content_document_id AS document_id
		FROM content_block AS block JOIN post AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
		UNION ALL
		SELECT block.id, 'page', owner.id, CAST(owner.status AS TEXT), owner.content_document_id FROM content_block AS block JOIN page AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
		UNION ALL
		SELECT block.id, 'work', owner.id, CAST(owner.status AS TEXT), owner.content_document_id FROM content_block AS block JOIN work AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
		UNION ALL
		SELECT block.id, 'program_event', owner.id, CAST(owner.status AS TEXT), owner.content_document_id FROM content_block AS block JOIN program_event AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
	`, blockIDs, blockIDs, blockIDs, blockIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	result := make(map[string]contentBlockDownloadOwnerWitness, len(rows))
	for _, row := range rows {
		counts[row.BlockID]++
		result[row.BlockID] = contentBlockDownloadOwnerWitness{
			ResourceType: row.ResourceType, ResourceID: row.ResourceID,
			Status: row.Status, DocumentID: row.DocumentID,
		}
	}
	for blockID, count := range counts {
		if count != 1 {
			delete(result, blockID)
		}
	}
	return result, nil
}

func lockContentBlockDownloadOwners(
	ctx context.Context,
	tx *gorm.DB,
	expected map[string]contentBlockDownloadOwnerWitness,
) (map[string]contentBlockDownloadOwnerWitness, error) {
	type target struct {
		BlockID string
		contentBlockDownloadOwnerWitness
	}
	targets := make([]target, 0, len(expected))
	for blockID, owner := range expected {
		targets = append(targets, target{BlockID: blockID, contentBlockDownloadOwnerWitness: owner})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ResourceType == targets[j].ResourceType {
			return targets[i].ResourceID < targets[j].ResourceID
		}
		return targets[i].ResourceType < targets[j].ResourceType
	})
	result := make(map[string]contentBlockDownloadOwnerWitness, len(targets))
	for _, target := range targets {
		var current contentBlockDownloadOwnerWitness
		query := tx.WithContext(ctx).Table(target.ResourceType).
			Select("? AS resource_type, id AS resource_id, status, content_document_id AS document_id", target.ResourceType).
			Where("id = ?", target.ResourceID).
			Clauses(clause.Locking{Strength: "SHARE"}).Take(&current)
		if query.Error != nil {
			if query.Error == gorm.ErrRecordNotFound {
				continue
			}
			return nil, query.Error
		}
		if current == target.contentBlockDownloadOwnerWitness {
			result[target.BlockID] = current
		}
	}
	return result, nil
}

func loadContentBlockMediaRelations(
	ctx context.Context,
	tx *gorm.DB,
	selectors []mediaasset.ContentBlockDownloadSelector,
	locked bool,
) (map[string]string, error) {
	requested := make(map[string]struct{}, len(selectors))
	blockIDs := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		blockID := strings.TrimSpace(selector.BlockID)
		referencePath := strings.TrimSpace(selector.ReferencePath)
		if blockID != "" && referencePath != "" {
			requested[mediaasset.ContentBlockDownloadPolicyKey(blockID, referencePath)] = struct{}{}
			blockIDs = append(blockIDs, blockID)
		}
	}
	if len(blockIDs) == 0 {
		return map[string]string{}, nil
	}
	var rows []struct {
		BlockID       string `gorm:"column:block_id"`
		ReferencePath string `gorm:"column:reference_path"`
		FileID        string `gorm:"column:file_id"`
	}
	query := tx.WithContext(ctx).
		Table("content_block_attachment AS attachment").
		Select("attachment.block_id, attachment.reference_path, attachment.file_id").
		Joins("JOIN file ON file.id = attachment.file_id").
		Where("attachment.block_id IN ? AND attachment.selector_kind = 'active' AND file.delete_requested_at IS NULL", blockIDs).
		Order("attachment.block_id, attachment.reference_path")
	if locked {
		query = query.Clauses(clause.Locking{Strength: "SHARE", Table: clause.Table{Name: "attachment"}})
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		key := mediaasset.ContentBlockDownloadPolicyKey(row.BlockID, row.ReferencePath)
		if _, ok := requested[key]; ok {
			result[key] = row.FileID
		}
	}
	return result, nil
}

// HydrateAuthorizedContentBlockMedia adds delivery to exact references only
// after the owning public domain has authorized and loaded its document.
func (s *FileService) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMedia(ctx, items)
}

func applyContentBlockDownloadDecision(
	item *contentv1.ContentBlockMediaItem,
	decision *openv1.FileDownloadAccess,
) {
	if item == nil || decision == nil {
		return
	}
	switch decision.GetAvailability() {
	case openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE:
		item.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE
	case openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE:
		item.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_UNAVAILABLE
	}
	switch decision.GetAction() {
	case openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD:
		item.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD
	case openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_SIGN_IN:
		item.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_SIGN_IN
	case openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE:
		item.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE
	}
}

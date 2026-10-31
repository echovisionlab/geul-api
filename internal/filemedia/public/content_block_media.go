package public

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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

	targets := contentMediaTargets(items)
	request := contentDownloadRequest{
		user:      auth.GetUser(ctx),
		selectors: make([]mediaasset.ContentBlockDownloadSelector, 0, len(targets)),
	}
	request.ownerAuthorization, request.ownerAuthorizationPresent = mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	for _, target := range targets {
		request.selectors = append(request.selectors, target.selector)
	}

	var evaluation downloadEvaluation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		evaluation, err = s.evaluateContentDownloads(ctx, tx, request)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.revalidateContentDownloads(ctx, tx, request, targets, evaluation)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// contentMediaTarget is an item that references an active file through a
// content Block selector. Other items are left as loaded.
type contentMediaTarget struct {
	item     *contentv1.ContentBlockMediaItem
	fileID   string
	selector mediaasset.ContentBlockDownloadSelector
	key      string
}

func contentMediaTargets(items []*contentv1.ContentBlockMediaItem) []contentMediaTarget {
	targets := make([]contentMediaTarget, 0, len(items))
	for _, item := range items {
		fileID := strings.TrimSpace(item.GetAttachment().GetActiveFileId())
		selector := item.GetSelector()
		if fileID == "" || selector == nil {
			continue
		}
		targets = append(targets, contentMediaTarget{
			item:   item,
			fileID: fileID,
			selector: mediaasset.ContentBlockDownloadSelector{
				BlockID: selector.GetBlockId(), ReferencePath: selector.GetReferencePath(),
			},
			key: mediaasset.ContentBlockDownloadPolicyKey(selector.GetBlockId(), selector.GetReferencePath()),
		})
	}
	return targets
}

type contentDownloadRequest struct {
	user                      *auth.UserInfo
	ownerAuthorization        mediaasset.ContentDownloadOwnerAuthorization
	ownerAuthorizationPresent bool
	selectors                 []mediaasset.ContentBlockDownloadSelector
}

// contentDownloadFacts is what one pass observed about the selected
// references; the locked pass reuses a decision only while they are unchanged.
// Maps are keyed by content Block download policy key, owners by Block ID.
type contentDownloadFacts struct {
	ownerAuthorized   bool
	owners            map[string]contentBlockDownloadOwnerWitness
	relations         map[string]string
	sources           map[string]mediaasset.FileDownloadSource
	fingerprints      map[string]string
	viewerFingerprint string
}

// downloadEvaluation is the unlocked pass: its facts and the download
// decision it made for each source.
type downloadEvaluation struct {
	contentDownloadFacts
	decisions map[string]*openv1.FileDownloadAccess
}

func (s *FileService) evaluateContentDownloads(
	ctx context.Context,
	tx *gorm.DB,
	request contentDownloadRequest,
) (downloadEvaluation, error) {
	var evaluation downloadEvaluation
	var err error
	evaluation.owners, err = loadContentBlockDownloadOwnerWitnesses(ctx, tx, request.selectors)
	if err != nil {
		return downloadEvaluation{}, errs.Internal(err)
	}
	evaluation.ownerAuthorized, err = s.validateContentDownloadOwnerAuthorization(
		ctx, tx, request.ownerAuthorization, request.ownerAuthorizationPresent, evaluation.owners, false,
	)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.relations, err = loadContentBlockMediaRelations(ctx, tx, request.selectors, false)
	if err != nil {
		return downloadEvaluation{}, errs.Internal(err)
	}
	evaluation.sources, err = mediaasset.LoadContentBlockDownloadSources(ctx, tx, request.selectors)
	if err != nil {
		return downloadEvaluation{}, errs.Internal(err)
	}
	allowed, err := mediaasset.EvaluateFileDownloadAccessBatch(ctx, tx, s.spiceDB, evaluation.sources, request.user, s.segments)
	if err != nil {
		return downloadEvaluation{}, errs.Internal(err)
	}
	signIn, err := contentDownloadSignInHints(ctx, tx, evaluation.sources, request.user)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.fingerprints, err = mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, evaluation.sources, s.segments)
	if err != nil {
		return downloadEvaluation{}, errs.Internal(err)
	}
	evaluation.viewerFingerprint, err = mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, evaluation.sources, request.user)
	if err != nil {
		return downloadEvaluation{}, errs.Internal(err)
	}
	evaluation.decisions = make(map[string]*openv1.FileDownloadAccess, len(request.selectors))
	for key, source := range evaluation.sources {
		if evaluation.ownerAuthorized {
			evaluation.decisions[key] = effectiveFileDownloadAccess(source.Audience, request.user, allowed[key], signIn[key])
		} else {
			evaluation.decisions[key] = unavailableFileDownloadAccess()
		}
	}
	return evaluation, nil
}

// contentDownloadSignInHints reports, for a reader without a member session,
// which sources signing in could open: authenticated-audience files and
// restricted files whose segments exist.
func contentDownloadSignInHints(
	ctx context.Context,
	tx *gorm.DB,
	sources map[string]mediaasset.FileDownloadSource,
	user *auth.UserInfo,
) (map[string]bool, error) {
	signIn := make(map[string]bool, len(sources))
	if user != nil && user.Authenticated && user.MemberID != "" {
		return signIn, nil
	}
	restricted := make(map[string]mediaasset.FileDownloadSource)
	for key, source := range sources {
		switch source.Audience {
		case mediaasset.FileDownloadAudienceAuthenticated:
			signIn[key] = true
		case mediaasset.FileDownloadAudienceRestricted:
			restricted[key] = source
		}
	}
	presence, err := mediaasset.RestrictedFileDownloadSegmentPresence(ctx, tx, restricted)
	if err != nil {
		return nil, errs.Internal(err)
	}
	for key, present := range presence {
		signIn[key] = present
	}
	return signIn, nil
}

// lockedContentDownloads is the locked pass's facts plus whether the viewer's
// account facts are still active.
type lockedContentDownloads struct {
	contentDownloadFacts
	viewerActive bool
}

// revalidateContentDownloads locks what the evaluation depended on, reloads
// it, and applies a delivery and download decision to each target. A target
// whose relation changed only gets the unavailable decision.
func (s *FileService) revalidateContentDownloads(
	ctx context.Context,
	tx *gorm.DB,
	request contentDownloadRequest,
	targets []contentMediaTarget,
	evaluation downloadEvaluation,
) error {
	current, err := s.lockContentDownloads(ctx, tx, request, evaluation)
	if err != nil {
		return err
	}
	decisions := make(map[string]*openv1.FileDownloadAccess, len(targets))
	deliveryAccess := make(map[string]resolvedMediaAccess, len(targets))
	stableRelations := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		decision, stable := reusableDecision(target, evaluation, current)
		decisions[target.key] = decision
		if stable {
			stableRelations[target.key] = struct{}{}
			deliveryAccess[target.fileID] = resolvedMediaAccess{}
		}
	}
	deliveries, err := s.buildScopedMediaURLsWithDB(ctx, tx, deliveryAccess)
	if err != nil {
		return err
	}
	for _, target := range targets {
		decision := decisions[target.key]
		if decision == nil {
			decision = unavailableFileDownloadAccess()
		}
		if _, stable := stableRelations[target.key]; !stable {
			applyContentBlockDownloadDecision(target.item, decision)
			continue
		}
		if err := s.applyDelivery(target.item, decision, deliveries[target.fileID], current.sources[target.key]); err != nil {
			return err
		}
	}
	return nil
}

// lockContentDownloads takes the policy, owner and viewer locks in the order
// every download path uses, then reloads the facts the evaluation used.
func (s *FileService) lockContentDownloads(
	ctx context.Context,
	tx *gorm.DB,
	request contentDownloadRequest,
	evaluation downloadEvaluation,
) (lockedContentDownloads, error) {
	var current lockedContentDownloads
	if err := mediaasset.LockFileDownloadPolicySegments(ctx, tx, evaluation.sources); err != nil {
		return current, errs.Internal(err)
	}
	var err error
	current.owners, err = lockContentBlockDownloadOwners(ctx, tx, evaluation.owners)
	if err != nil {
		return current, errs.Internal(err)
	}
	current.ownerAuthorized, err = s.validateContentDownloadOwnerAuthorization(
		ctx, tx, request.ownerAuthorization, request.ownerAuthorizationPresent, current.owners, true,
	)
	if err != nil {
		return current, err
	}
	current.viewerActive, err = mediaasset.LockFileDownloadViewerFacts(ctx, tx, evaluation.sources, request.user)
	if err != nil {
		return current, errs.Internal(err)
	}
	current.relations, err = loadContentBlockMediaRelations(ctx, tx, request.selectors, true)
	if err != nil {
		return current, errs.Internal(err)
	}
	current.sources, err = mediaasset.LoadContentBlockDownloadSources(ctx, tx, request.selectors)
	if err != nil {
		return current, errs.Internal(err)
	}
	current.fingerprints, err = mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, current.sources, s.segments)
	if err != nil {
		return current, errs.Internal(err)
	}
	current.viewerFingerprint, err = mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, current.sources, request.user)
	if err != nil {
		return current, errs.Internal(err)
	}
	return current, nil
}

// reusableDecision returns the evaluated decision for target when the locked
// pass still sees the same owner, relation, source and policy, and the
// unavailable decision otherwise. stable reports whether the owner and
// relation held, which is what lets the target receive a delivery.
func reusableDecision(
	target contentMediaTarget,
	evaluated downloadEvaluation,
	current lockedContentDownloads,
) (decision *openv1.FileDownloadAccess, stable bool) {
	blockID, key := target.selector.BlockID, target.key
	evaluatedOwner, evaluatedOwnerExists := evaluated.owners[blockID]
	currentOwner, currentOwnerExists := current.owners[blockID]
	evaluatedRelationFileID, evaluatedRelationExists := evaluated.relations[key]
	currentRelationFileID, currentRelationExists := current.relations[key]
	stable = evaluated.ownerAuthorized && current.ownerAuthorized &&
		evaluatedRelationExists && currentRelationExists &&
		evaluatedOwnerExists && currentOwnerExists && evaluatedOwner == currentOwner &&
		evaluatedRelationFileID == target.fileID && currentRelationFileID == target.fileID
	if !stable {
		return unavailableFileDownloadAccess(), false
	}
	source, exists := current.sources[key]
	evaluatedSource, evaluatedExists := evaluated.sources[key]
	if !exists || !evaluatedExists ||
		source.FileID != target.fileID || evaluatedSource.FileID != target.fileID ||
		current.fingerprints[key] != evaluated.fingerprints[key] {
		return unavailableFileDownloadAccess(), true
	}
	if fileDownloadDecisionDependsOnActiveViewer(evaluatedSource, evaluated.decisions[key]) &&
		(!current.viewerActive || current.viewerFingerprint != evaluated.viewerFingerprint) {
		return unavailableFileDownloadAccess(), true
	}
	return evaluated.decisions[key], true
}

// applyDelivery sets item's delivery, signing a download URL for source when
// the decision allows a download, and then applies the decision.
func (s *FileService) applyDelivery(
	item *contentv1.ContentBlockMediaItem,
	decision *openv1.FileDownloadAccess,
	delivery *commonv1.MediaDelivery,
	source mediaasset.FileDownloadSource,
) error {
	if delivery != nil {
		item.Delivery = proto.Clone(delivery).(*commonv1.MediaDelivery)
		if decision.GetAction() == openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD {
			download, err := mediaauth.ExpiringFileRef(
				s.mediaDomain, s.mediaSecret, source.FileID, source.Extension,
				source.MimeType, source.FileName, mediaauth.PurposeDownload, s.effectiveDownloadTTL(),
			)
			if err != nil {
				return errs.Internal(err)
			}
			item.Delivery.Download = download
		}
	}
	applyContentBlockDownloadDecision(item, decision)
	return nil
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
		if !contentDownloadOwnerStatusIsPublic(authorization.ResourceType, authorization.Status) {
			return false, nil
		}
		if authorization.ResourceType == "page" {
			reason, err := pageaccess.EvaluateStored(ctx, db, s.spiceDB, authorization.ResourceID)
			return reason == commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, err
		}
		return true, nil
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	if err := query.Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
			Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&current)
		if query.Error != nil {
			if errors.Is(query.Error, gorm.ErrRecordNotFound) {
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare, Table: clause.Table{Name: "attachment"}})
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

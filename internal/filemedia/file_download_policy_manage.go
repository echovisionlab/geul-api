package filemedia

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const maxFileDownloadAudienceSegments = 20

type filePolicyOwnerAction uint8

const (
	filePolicyOwnerView filePolicyOwnerAction = iota + 1
	filePolicyOwnerEdit
)

type fileDownloadPolicySelector struct {
	EntityType    managev1.TranscodeEntityType
	EntityID      string
	BlockID       *string
	ReferencePath *string
}

type fileDownloadPolicyRelation struct {
	Selector     fileDownloadPolicySelector      `gorm:"-"`
	FileID       string                          `gorm:"column:file_id"`
	Audience     mediaasset.FileDownloadAudience `gorm:"column:download_audience"`
	ResourceType string                          `gorm:"column:resource_type"`
	ResourceID   string                          `gorm:"column:resource_id"`
}

// FileDownloadPolicyTarget names one File attachment: a File Block of a
// document, or a Track's original audio.
type FileDownloadPolicyTarget struct {
	EntityType    managev1.TranscodeEntityType
	EntityID      string
	BlockID       *string
	ReferencePath *string
}

func (t FileDownloadPolicyTarget) selector() (fileDownloadPolicySelector, error) {
	return validateFileDownloadPolicySelector(t.EntityType, t.EntityID, t.BlockID, t.ReferencePath)
}

// GetFileDownloadPolicyQuery reads the download policy of one attachment.
type GetFileDownloadPolicyQuery struct {
	Target FileDownloadPolicyTarget
}

func (s *Service) GetFileDownloadPolicy(ctx context.Context, query GetFileDownloadPolicyQuery) (*managev1.FileDownloadPolicy, error) {
	selector, err := query.Target.selector()
	if err != nil {
		return nil, err
	}
	var policy *managev1.FileDownloadPolicy
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		relation, resolveErr := s.resolveFileDownloadPolicyRelation(ctx, tx, selector, false)
		if resolveErr != nil {
			return resolveErr
		}
		if authorizeErr := s.authorizeFileDownloadPolicyOwner(ctx, tx, relation, filePolicyOwnerView); authorizeErr != nil {
			return authorizeErr
		}
		var loadErr error
		policy, loadErr = s.loadManageFileDownloadPolicy(ctx, tx, relation)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

// FileDownloadPolicyState is a download audience with its segments.
type FileDownloadPolicyState struct {
	Audience           managev1.FileDownloadAudience
	AudienceSegmentIDs []string
}

// UpdateFileDownloadPolicyCommand sets the download policy of the attachment
// that still holds ExpectedFileID. Observed is the policy the caller read; a
// concurrent change to a field the caller did not change is kept.
type UpdateFileDownloadPolicyCommand struct {
	Target         FileDownloadPolicyTarget
	ExpectedFileID string
	Policy         FileDownloadPolicyState
	// Observed is required.
	Observed *FileDownloadPolicyState
}

// fileDownloadPolicyChange is a validated UpdateFileDownloadPolicyCommand.
type fileDownloadPolicyChange struct {
	selector           fileDownloadPolicySelector
	expectedFileID     string
	audience           mediaasset.FileDownloadAudience
	segmentIDs         []string
	observedAudience   mediaasset.FileDownloadAudience
	observedSegmentIDs []string
}

func validateFileDownloadPolicyChange(command UpdateFileDownloadPolicyCommand) (fileDownloadPolicyChange, error) {
	selector, err := command.Target.selector()
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	parsedExpectedFileID, parseErr := uuid.Parse(strings.TrimSpace(command.ExpectedFileID))
	if parseErr != nil {
		return fileDownloadPolicyChange{}, errs.InvalidArgument("expected_file_id", "must be a valid UUID")
	}
	audience, ok := storedFileDownloadAudience(command.Policy.Audience)
	if !ok {
		return fileDownloadPolicyChange{}, errs.InvalidArgument("audience", "unsupported file download audience")
	}
	segmentIDs, err := uniqueUUIDs(command.Policy.AudienceSegmentIDs, "audience_segment_ids")
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	if command.Observed == nil {
		return fileDownloadPolicyChange{}, errs.InvalidArgument("observed_policy", "is required")
	}
	observedAudience, ok := storedFileDownloadAudience(command.Observed.Audience)
	if !ok {
		return fileDownloadPolicyChange{}, errs.InvalidArgument("observed_policy.audience", "unsupported file download audience")
	}
	observedSegmentIDs, err := uniqueUUIDs(command.Observed.AudienceSegmentIDs, "observed_policy.audience_segment_ids")
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	if len(observedSegmentIDs) > maxFileDownloadAudienceSegments {
		return fileDownloadPolicyChange{}, tooManyFileDownloadAudienceSegments("observed_policy.audience_segment_ids")
	}
	if len(segmentIDs) > maxFileDownloadAudienceSegments {
		return fileDownloadPolicyChange{}, tooManyFileDownloadAudienceSegments("audience_segment_ids")
	}
	if audience != mediaasset.FileDownloadAudienceRestricted && len(segmentIDs) > 0 {
		return fileDownloadPolicyChange{}, errs.InvalidArgument("audience_segment_ids", "audience segments require restricted audience")
	}
	slices.Sort(segmentIDs)
	return fileDownloadPolicyChange{
		selector: selector, expectedFileID: parsedExpectedFileID.String(),
		audience: audience, segmentIDs: segmentIDs,
		observedAudience: observedAudience, observedSegmentIDs: observedSegmentIDs,
	}, nil
}

func tooManyFileDownloadAudienceSegments(field string) error {
	return errs.InvalidArgument(field, fmt.Sprintf("at most %d audience segments are allowed", maxFileDownloadAudienceSegments))
}

func (s *Service) UpdateFileDownloadPolicy(ctx context.Context, command UpdateFileDownloadPolicyCommand) (*managev1.FileDownloadPolicy, error) {
	change, err := validateFileDownloadPolicyChange(command)
	if err != nil {
		return nil, err
	}
	if err := requireFileDownloadPolicyActor(ctx); err != nil {
		return nil, err
	}
	// Authorize the exact owner before validating Segment IDs so an unrelated
	// caller cannot probe Audience state. The locked check is repeated inside
	// the mutation transaction after Segment locks are acquired.
	initial, err := s.resolveFileDownloadPolicyRelation(ctx, s.db, change.selector, false)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.authorizeFileDownloadPolicyOwner(ctx, tx, initial, filePolicyOwnerEdit)
	}); err != nil {
		return nil, err
	}
	var policy *managev1.FileDownloadPolicy
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var applyErr error
		policy, applyErr = s.applyFileDownloadPolicyChange(ctx, tx, change)
		return applyErr
	})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func (s *Service) applyFileDownloadPolicyChange(ctx context.Context, tx *gorm.DB, change fileDownloadPolicyChange) (*managev1.FileDownloadPolicy, error) {
	// Audience Segment rows are the first database lock in policy and archive
	// flows. This stable order prevents archive-versus-policy deadlocks.
	if err := s.audienceAccess.ValidateAuthenticatedSegmentIDs(ctx, tx, change.segmentIDs); err != nil {
		return nil, err
	}
	resolved, err := s.resolveFileDownloadPolicyRelation(ctx, tx, change.selector, false)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeFileDownloadPolicyOwner(ctx, tx, resolved, filePolicyOwnerEdit); err != nil {
		return nil, err
	}
	locked, err := s.resolveFileDownloadPolicyRelation(ctx, tx, change.selector, true)
	if err != nil {
		return nil, err
	}
	if locked.FileID != change.expectedFileID {
		return nil, errs.FailedPrecondition("File attachment changed; reload before saving download policy")
	}
	previousSegmentIDs, err := loadFileDownloadPolicySegmentIDs(ctx, tx, locked)
	if err != nil {
		return nil, err
	}
	nextAudience, nextSegmentIDs := mergeObservedFileDownloadPolicy(
		locked.Audience, change.observedAudience, change.audience, previousSegmentIDs, change.observedSegmentIDs, change.segmentIDs,
	)
	if len(nextSegmentIDs) > maxFileDownloadAudienceSegments {
		return nil, tooManyFileDownloadAudienceSegments("audience_segment_ids")
	}
	if locked.Audience == nextAudience && slices.Equal(previousSegmentIDs, nextSegmentIDs) {
		return s.loadManageFileDownloadPolicy(ctx, tx, locked)
	}
	if err := updateFileDownloadPolicyRelation(ctx, tx, locked, nextAudience, nextSegmentIDs); err != nil {
		return nil, err
	}
	if err := appendRelationFileDownloadPolicyAudit(
		ctx, tx, s.auditWriter, locked, locked.Audience, nextAudience, previousSegmentIDs, nextSegmentIDs,
	); err != nil {
		return nil, err
	}
	if err := s.publishFileDownloadPolicyChanged(ctx, tx, locked); err != nil {
		return nil, err
	}
	locked.Audience = nextAudience
	return s.loadManageFileDownloadPolicy(ctx, tx, locked)
}

// Policy is attachment metadata. Track policies belong to their Release, not
// to a Track room. PublishInTransaction retains the owner's current document
// revision and stamps the browser origin; no document or locale state advances.
func (s *Service) publishFileDownloadPolicyChanged(ctx context.Context, tx *gorm.DB, relation fileDownloadPolicyRelation) error {
	event, err := fileDownloadPolicyChangedEvent(relation)
	if err != nil {
		return err
	}
	publisher, ok := s.asyncPublisher.(contentsignal.Publisher)
	if !ok {
		return contentsignal.ErrPublisherRequired
	}
	return contentsignal.PublishInTransaction(ctx, tx, publisher, event)
}

func fileDownloadPolicyChangedEvent(relation fileDownloadPolicyRelation) (*managev1.ContentUpdatedEvent, error) {
	entityTypes := map[string]managev1.ContentEntityType{
		"post":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		"page":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		"work":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		"program_event": managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT,
		"release":       managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE,
	}
	entityType, ok := entityTypes[relation.ResourceType]
	if !ok || relation.ResourceID == "" {
		return nil, fmt.Errorf("unsupported File download policy signal owner %s", relation.ResourceType)
	}
	return &managev1.ContentUpdatedEvent{
		EntityType: entityType, EntityId: relation.ResourceID,
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		DocumentStateChanged: false, TimestampMs: time.Now().UnixMilli(),
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "file_download_policy", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION,
		}},
	}, nil
}

func mergeObservedFileDownloadPolicy(
	currentAudience, observedAudience, desiredAudience mediaasset.FileDownloadAudience,
	currentSegmentIDs, observedSegmentIDs, desiredSegmentIDs []string,
) (mediaasset.FileDownloadAudience, []string) {
	mergedAudience := currentAudience
	if observedAudience != desiredAudience {
		mergedAudience = desiredAudience
	}
	mergedSegmentIDs := mergeObservedFileDownloadSegmentIDs(currentSegmentIDs, observedSegmentIDs, desiredSegmentIDs)
	if mergedAudience != mediaasset.FileDownloadAudienceRestricted {
		// Non-restricted audiences cannot retain segment relations.
		mergedSegmentIDs = nil
	}
	return mergedAudience, mergedSegmentIDs
}

func mergeObservedFileDownloadSegmentIDs(current, observed, desired []string) []string {
	result := make(map[string]struct{}, len(current)+len(desired))
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	for _, id := range current {
		result[id] = struct{}{}
	}
	for _, id := range observed {
		observedSet[id] = struct{}{}
	}
	for _, id := range desired {
		desiredSet[id] = struct{}{}
	}
	for id := range observedSet {
		if _, shouldKeep := desiredSet[id]; !shouldKeep {
			delete(result, id)
		}
	}
	for id := range desiredSet {
		if _, existed := observedSet[id]; !existed {
			result[id] = struct{}{}
		}
	}
	merged := make([]string, 0, len(result))
	for id := range result {
		merged = append(merged, id)
	}
	sort.Strings(merged)
	return merged
}

func validateFileDownloadPolicySelector(
	entityType managev1.TranscodeEntityType,
	entityID string,
	blockID *string,
	referencePath *string,
) (fileDownloadPolicySelector, error) {
	entityID = strings.TrimSpace(entityID)
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		return fileDownloadPolicySelector{}, errs.Required("entity_type")
	}
	parsedEntityID, err := uuid.Parse(entityID)
	if err != nil {
		return fileDownloadPolicySelector{}, errs.InvalidArgument("entity_id", "must be a valid UUID")
	}
	entityID = parsedEntityID.String()
	selector := fileDownloadPolicySelector{EntityType: entityType, EntityID: entityID}
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		if blockID != nil || referencePath != nil {
			return fileDownloadPolicySelector{}, errs.InvalidArgument("block_id", "Track policy does not accept a Content Block selector")
		}
		return selector, nil
	}
	if _, _, ok := contentBlockFilePolicyOwner(entityType); !ok {
		return fileDownloadPolicySelector{}, errs.InvalidArgument("entity_type", "unsupported file download policy owner")
	}
	if blockID == nil {
		return fileDownloadPolicySelector{}, errs.Required("block_id")
	}
	if referencePath == nil {
		return fileDownloadPolicySelector{}, errs.Required("reference_path")
	}
	normalizedBlockID := strings.TrimSpace(*blockID)
	parsedBlockID, err := uuid.Parse(normalizedBlockID)
	if err != nil {
		return fileDownloadPolicySelector{}, errs.InvalidArgument("block_id", "must be a valid UUID")
	}
	normalizedBlockID = parsedBlockID.String()
	normalizedReferencePath := strings.TrimSpace(*referencePath)
	if normalizedReferencePath != "file" {
		return fileDownloadPolicySelector{}, errs.InvalidArgument("reference_path", "File Block policy requires the canonical file selector")
	}
	selector.BlockID = &normalizedBlockID
	selector.ReferencePath = &normalizedReferencePath
	return selector, nil
}

func (s *Service) resolveFileDownloadPolicyRelation(
	ctx context.Context,
	db *gorm.DB,
	selector fileDownloadPolicySelector,
	lock bool,
) (fileDownloadPolicyRelation, error) {
	if table, resourceType, ok := contentBlockFilePolicyOwner(selector.EntityType); ok {
		var row fileDownloadPolicyRelation
		query := db.WithContext(ctx).
			Table("content_block_attachment AS attachment").
			Select("attachment.file_id, attachment.download_audience, ? AS resource_type, owner.id AS resource_id", resourceType).
			Joins("JOIN content_block AS block ON block.id = attachment.block_id").
			Joins("JOIN "+table+" AS owner ON owner.content_document_id = block.document_id").
			Joins("JOIN file ON file.id = attachment.file_id AND file.delete_requested_at IS NULL").
			Where("owner.id = ?", selector.EntityID).
			Where("block.id = ? AND block.kind = 'file'", *selector.BlockID).
			Where("attachment.reference_path = ? AND attachment.selector_kind = 'active'", *selector.ReferencePath)
		if lock {
			query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "attachment"}})
		}
		if err := query.Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fileDownloadPolicyRelation{}, errs.NotFound("File Block attachment", *selector.BlockID)
			}
			return fileDownloadPolicyRelation{}, errs.Internal(err)
		}
		row.Selector = selector
		return row, nil
	}
	var row fileDownloadPolicyRelation
	query := db.WithContext(ctx).
		Table("track").
		Select("track.audio_original_file_id AS file_id, track.download_audience, 'release' AS resource_type, track.release_id AS resource_id").
		Joins("JOIN file ON file.id = track.audio_original_file_id AND file.delete_requested_at IS NULL").
		Where("track.id = ?", selector.EntityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "track"}})
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fileDownloadPolicyRelation{}, errs.NotFound("Track original audio", selector.EntityID)
		}
		return fileDownloadPolicyRelation{}, errs.Internal(err)
	}
	row.Selector = selector
	return row, nil
}

func (s *Service) authorizeFileDownloadPolicyOwner(
	ctx context.Context,
	db *gorm.DB,
	relation fileDownloadPolicyRelation,
	action filePolicyOwnerAction,
) error {
	if relation.ResourceType == "post" {
		access := s.postAccess
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	if relation.ResourceType == "page" {
		access := s.pagePolicyAccess
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	if relation.ResourceType == "program_event" {
		access := s.programEventAttachment
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, s.spiceDB, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, s.spiceDB, relation.ResourceID)
	}
	if relation.ResourceType == "work" {
		access := s.workPolicyAccess
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	if relation.ResourceType == "release" {
		access := s.releasePolicyAccess
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	return errs.Internal(fmt.Errorf("unsupported File policy owner %s", relation.ResourceType))
}

func requireFileDownloadPolicyActor(ctx context.Context) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return errs.AuthenticationRequired()
	}
	if user.Banned {
		return errs.AccountBanned()
	}
	return nil
}

func contentBlockFilePolicyOwner(entityType managev1.TranscodeEntityType) (string, string, bool) {
	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		return "post", "post", true
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		return "page", "page", true
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		return "work", "work", true
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT:
		return "program_event", "program_event", true
	default:
		return "", "", false
	}
}

func loadFileDownloadPolicySegmentIDs(ctx context.Context, db *gorm.DB, relation fileDownloadPolicyRelation) ([]string, error) {
	var ids []string
	query := db.WithContext(ctx)
	if relation.Selector.EntityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		query = query.Table("track_download_audience_segment").Where("track_id = ?", relation.Selector.EntityID)
	} else {
		query = query.Table("content_block_attachment_download_audience_segment").
			Where("block_id = ? AND reference_path = ?", *relation.Selector.BlockID, *relation.Selector.ReferencePath)
	}
	if err := query.Order("audience_segment_id ASC").Pluck("audience_segment_id", &ids).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return ids, nil
}

func updateFileDownloadPolicyRelation(
	ctx context.Context,
	tx *gorm.DB,
	relation fileDownloadPolicyRelation,
	audience mediaasset.FileDownloadAudience,
	segmentIDs []string,
) error {
	if relation.Selector.EntityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		if err := tx.WithContext(ctx).Model(&model.Track{}).
			Where("id = ?", relation.Selector.EntityID).
			Update("download_audience", string(audience)).Error; err != nil {
			return errs.Internal(err)
		}
		if err := tx.WithContext(ctx).Where("track_id = ?", relation.Selector.EntityID).
			Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
			return errs.Internal(err)
		}
		if len(segmentIDs) == 0 {
			return nil
		}
		now := time.Now().UTC()
		rows := make([]model.TrackDownloadAudienceSegment, 0, len(segmentIDs))
		for _, segmentID := range segmentIDs {
			rows = append(rows, model.TrackDownloadAudienceSegment{TrackID: relation.Selector.EntityID, AudienceSegmentID: segmentID, CreatedAt: now})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return errs.Internal(err)
		}
		return nil
	}
	if err := tx.WithContext(ctx).Table("content_block_attachment").
		Where("block_id = ? AND reference_path = ?", *relation.Selector.BlockID, *relation.Selector.ReferencePath).
		Update("download_audience", string(audience)).Error; err != nil {
		return errs.Internal(err)
	}
	if err := tx.WithContext(ctx).
		Where("block_id = ? AND reference_path = ?", *relation.Selector.BlockID, *relation.Selector.ReferencePath).
		Delete(&model.ContentBlockAttachmentDownloadAudienceSegment{}).Error; err != nil {
		return errs.Internal(err)
	}
	if len(segmentIDs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]model.ContentBlockAttachmentDownloadAudienceSegment, 0, len(segmentIDs))
	for _, segmentID := range segmentIDs {
		rows = append(rows, model.ContentBlockAttachmentDownloadAudienceSegment{
			BlockID: *relation.Selector.BlockID, ReferencePath: *relation.Selector.ReferencePath,
			AudienceSegmentID: segmentID, CreatedAt: now,
		})
	}
	if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func (s *Service) loadManageFileDownloadPolicy(
	ctx context.Context,
	db *gorm.DB,
	relation fileDownloadPolicyRelation,
) (*managev1.FileDownloadPolicy, error) {
	audience, ok := manageFileDownloadAudience(relation.Audience)
	if !ok {
		return nil, errs.InternalMsg("attachment has invalid download audience")
	}
	segmentIDs, err := loadFileDownloadPolicySegmentIDs(ctx, db, relation)
	if err != nil {
		return nil, err
	}
	var segments []model.AudienceSegment
	if len(segmentIDs) > 0 {
		if err := db.WithContext(ctx).
			Where("id IN ? AND archived_at IS NULL", segmentIDs).
			Order("LOWER(name) ASC, id ASC").Find(&segments).Error; err != nil {
			return nil, errs.Internal(err)
		}
	}
	audienceAccess := s.audienceAccess
	summaries := make([]*managev1.AudienceSegmentSummary, 0, len(segments))
	for i := range segments {
		summary, valid := audienceAccess.AuthenticatedSegmentSummary(&segments[i])
		if !valid {
			return nil, errs.InternalMsg("attachment references an ineligible audience segment")
		}
		summaries = append(summaries, summary)
	}
	return &managev1.FileDownloadPolicy{
		EntityType:       relation.Selector.EntityType,
		EntityId:         relation.Selector.EntityID,
		BlockId:          relation.Selector.BlockID,
		ReferencePath:    relation.Selector.ReferencePath,
		FileId:           relation.FileID,
		Audience:         audience,
		AudienceSegments: summaries,
	}, nil
}

func appendRelationFileDownloadPolicyAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	relation fileDownloadPolicyRelation,
	previousAudience, audience mediaasset.FileDownloadAudience,
	previousSegmentIDs, segmentIDs []string,
) error {
	var action sharedtelemetry.AuditAction
	var build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)
	previous := sharedtelemetry.AuditState(previousAudience)
	next := sharedtelemetry.AuditState(audience)
	switch relation.ResourceType {
	case "post":
		action = sharedtelemetry.AuditPostUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "page":
		action = sharedtelemetry.AuditPageUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "work":
		action = sharedtelemetry.AuditWorkUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "program_event":
		action = sharedtelemetry.AuditProgramEventUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "release":
		action = sharedtelemetry.AuditReleaseUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackDownloadPolicyAuditRecord(m, relation.ResourceID, relation.Selector.EntityID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	default:
		return errs.InternalMsg("unsupported File download policy audit owner")
	}
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, action, build)
}

func requireFileDownloadAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return errs.AuthenticationRequired()
	}
	if user.Banned {
		return errs.AccountBanned()
	}
	can, err := policyv1.File.List()
	if err != nil {
		return errs.WithCause(errs.AuthorRequired(), err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return errs.AuthorRequired()
	}
	return nil
}

func uniqueUUIDs(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value := strings.TrimSpace(rawValue)
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, errs.InvalidArgument(field, fmt.Sprintf("invalid UUID: %s", value))
		}
		value = parsed.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func manageFileDownloadAudience(audience mediaasset.FileDownloadAudience) (managev1.FileDownloadAudience, bool) {
	switch audience {
	case mediaasset.FileDownloadAudienceDisabled:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED, true
	case mediaasset.FileDownloadAudiencePublic:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC, true
	case mediaasset.FileDownloadAudienceAuthenticated:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_AUTHENTICATED, true
	case mediaasset.FileDownloadAudienceRestricted:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, true
	default:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_UNSPECIFIED, false
	}
}

func storedFileDownloadAudience(audience managev1.FileDownloadAudience) (mediaasset.FileDownloadAudience, bool) {
	switch audience {
	case managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED:
		return mediaasset.FileDownloadAudienceDisabled, true
	case managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC:
		return mediaasset.FileDownloadAudiencePublic, true
	case managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_AUTHENTICATED:
		return mediaasset.FileDownloadAudienceAuthenticated, true
	case managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED:
		return mediaasset.FileDownloadAudienceRestricted, true
	default:
		return "", false
	}
}

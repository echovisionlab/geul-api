package filemedia

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
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

func (s *FileService) GetFileDownloadPolicy(
	ctx context.Context,
	req *connect.Request[managev1.GetFileDownloadPolicyRequest],
) (*connect.Response[managev1.GetFileDownloadPolicyResponse], error) {
	selector, err := validateFileDownloadPolicySelector(
		req.Msg.EntityType, req.Msg.EntityId, req.Msg.BlockId, req.Msg.ReferencePath,
	)
	if err != nil {
		return nil, err
	}
	var relation fileDownloadPolicyRelation
	var policy *managev1.FileDownloadPolicy
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resolved, resolveErr := s.resolveFileDownloadPolicyRelation(ctx, tx, selector, false)
		if resolveErr != nil {
			return resolveErr
		}
		if authorizeErr := s.authorizeFileDownloadPolicyOwner(ctx, tx, resolved, filePolicyOwnerView); authorizeErr != nil {
			return authorizeErr
		}
		relation = resolved
		var loadErr error
		policy, loadErr = s.loadManageFileDownloadPolicy(ctx, tx, relation)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetFileDownloadPolicyResponse{Policy: policy}), nil
}

func (s *FileService) UpdateFileDownloadPolicy(
	ctx context.Context,
	req *connect.Request[managev1.UpdateFileDownloadPolicyRequest],
) (*connect.Response[managev1.UpdateFileDownloadPolicyResponse], error) {
	selector, err := validateFileDownloadPolicySelector(
		req.Msg.EntityType, req.Msg.EntityId, req.Msg.BlockId, req.Msg.ReferencePath,
	)
	if err != nil {
		return nil, err
	}
	expectedFileID := strings.TrimSpace(req.Msg.ExpectedFileId)
	parsedExpectedFileID, parseErr := uuid.Parse(expectedFileID)
	if parseErr != nil {
		return nil, errs.InvalidArgument("expected_file_id", "must be a valid UUID")
	}
	expectedFileID = parsedExpectedFileID.String()
	audience, ok := storedFileDownloadAudience(req.Msg.Audience)
	if !ok {
		return nil, errs.InvalidArgument("audience", "unsupported file download audience")
	}
	segmentIDs, err := uniqueUUIDs(req.Msg.AudienceSegmentIds, "audience_segment_ids")
	if err != nil {
		return nil, err
	}
	if len(segmentIDs) > maxFileDownloadAudienceSegments {
		return nil, errs.InvalidArgument(
			"audience_segment_ids",
			fmt.Sprintf("at most %d audience segments are allowed", maxFileDownloadAudienceSegments),
		)
	}
	if audience != mediaasset.FileDownloadAudienceRestricted && len(segmentIDs) > 0 {
		return nil, errs.InvalidArgument("audience_segment_ids", "audience segments require restricted audience")
	}
	slices.Sort(segmentIDs)
	if err := requireFileDownloadPolicyActor(ctx); err != nil {
		return nil, err
	}
	// Authorize the exact owner before validating Segment IDs so an unrelated
	// caller cannot probe Audience state. The locked check is repeated inside
	// the mutation transaction after Segment locks are acquired.
	initial, err := s.resolveFileDownloadPolicyRelation(ctx, s.db, selector, false)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.authorizeFileDownloadPolicyOwner(ctx, tx, initial, filePolicyOwnerEdit)
	}); err != nil {
		return nil, err
	}

	var relation fileDownloadPolicyRelation
	var policy *managev1.FileDownloadPolicy
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		audienceAccess, dependencyErr := requireAudienceAccess(s.audienceAccess)
		if dependencyErr != nil {
			return dependencyErr
		}
		// Audience Segment rows are the first database lock in policy and archive
		// flows. This stable order prevents archive-versus-policy deadlocks.
		if validateErr := audienceAccess.ValidateAuthenticatedSegmentIDs(ctx, tx, segmentIDs); validateErr != nil {
			return validateErr
		}
		resolved, resolveErr := s.resolveFileDownloadPolicyRelation(ctx, tx, selector, false)
		if resolveErr != nil {
			return resolveErr
		}
		if authorizeErr := s.authorizeFileDownloadPolicyOwner(ctx, tx, resolved, filePolicyOwnerEdit); authorizeErr != nil {
			return authorizeErr
		}
		locked, lockErr := s.resolveFileDownloadPolicyRelation(ctx, tx, selector, true)
		if lockErr != nil {
			return lockErr
		}
		if locked.FileID != expectedFileID {
			return errs.FailedPrecondition("File attachment changed; reload before saving download policy")
		}
		previousSegmentIDs, loadErr := loadFileDownloadPolicySegmentIDs(ctx, tx, locked)
		if loadErr != nil {
			return loadErr
		}
		if locked.Audience == audience && slices.Equal(previousSegmentIDs, segmentIDs) {
			relation = locked
			policy, loadErr = s.loadManageFileDownloadPolicy(ctx, tx, relation)
			return loadErr
		}
		if updateErr := updateFileDownloadPolicyRelation(ctx, tx, locked, audience, segmentIDs); updateErr != nil {
			return updateErr
		}
		if auditErr := appendRelationFileDownloadPolicyAudit(
			ctx, tx, s.auditWriter, locked, locked.Audience, audience, previousSegmentIDs, segmentIDs,
		); auditErr != nil {
			return auditErr
		}
		locked.Audience = audience
		relation = locked
		policy, loadErr = s.loadManageFileDownloadPolicy(ctx, tx, relation)
		return loadErr
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.UpdateFileDownloadPolicyResponse{Policy: policy}), nil
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

func (s *FileService) resolveFileDownloadPolicyRelation(
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
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "attachment"}})
		}
		if err := query.Take(&row).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
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
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "track"}})
	}
	if err := query.Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fileDownloadPolicyRelation{}, errs.NotFound("Track original audio", selector.EntityID)
		}
		return fileDownloadPolicyRelation{}, errs.Internal(err)
	}
	row.Selector = selector
	return row, nil
}

func (s *FileService) authorizeFileDownloadPolicyOwner(
	ctx context.Context,
	db *gorm.DB,
	relation fileDownloadPolicyRelation,
	action filePolicyOwnerAction,
) error {
	if relation.ResourceType == "post" {
		access, err := requirePostAccess(s.postAccess)
		if err != nil {
			return err
		}
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	if relation.ResourceType == "page" {
		access, err := requirePagePolicyAccess(s.pagePolicyAccess)
		if err != nil {
			return err
		}
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	if relation.ResourceType == "program_event" {
		access, err := requireProgramEventAttachment(s.programEventAttachment)
		if err != nil {
			return err
		}
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, s.spiceDB, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, s.spiceDB, relation.ResourceID)
	}
	if relation.ResourceType == "work" {
		access, err := requireWorkPolicyAccess(s.workPolicyAccess)
		if err != nil {
			return err
		}
		if action == filePolicyOwnerView {
			return access.RequireLockedView(ctx, db, relation.ResourceID)
		}
		return access.RequireLockedEdit(ctx, db, relation.ResourceID)
	}
	if relation.ResourceType == "release" {
		access, err := requireReleasePolicyAccess(s.releasePolicyAccess)
		if err != nil {
			return err
		}
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

func (s *FileService) loadManageFileDownloadPolicy(
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
	audienceAccess, err := requireAudienceAccess(s.audienceAccess)
	if err != nil {
		return nil, err
	}
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

func requireFileDownloadAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient) (*auth.UserInfo, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return nil, errs.AuthenticationRequired()
	}
	if user.Banned {
		return nil, errs.AccountBanned()
	}
	can, err := policyv1.File.List()
	if err != nil {
		return nil, errs.AuthorRequired()
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return nil, errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return nil, errs.AuthorRequired()
	}
	return user, nil
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

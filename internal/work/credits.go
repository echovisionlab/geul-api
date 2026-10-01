package work

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// workSortConfig defines allowed sort fields for works
func (s *WorkService) GetWorkCredits(
	ctx context.Context,
	req *connect.Request[managev1.GetWorkCreditsRequest],
) (*connect.Response[managev1.GetWorkCreditsResponse], error) {
	// Verify work exists
	var work model.Work
	if err := s.db.WithContext(ctx).
		Where("id = ?", req.Msg.WorkId).
		First(&work).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.WorkId)
		}
		return nil, errs.Internal(err)
	}
	if err := requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.View, workAuthorizationRead); err != nil {
		return nil, err
	}

	var groups []model.WorkCreditGroup
	var credits []model.WorkCredit
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		groups, credits, _, _, err = loadWorkCreditRows(ctx, tx, req.Msg.WorkId)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}); err != nil {
		return nil, errs.Wrap(err)
	}

	protoGroups := make([]*managev1.WorkCreditGroup, len(groups))
	for i, g := range groups {
		protoGroups[i] = s.toProtoCreditGroup(&g)
	}

	protoCredits := make([]*managev1.WorkCredit, len(credits))
	for i := range credits {
		protoCredits[i] = s.toProtoCredit(ctx, &credits[i])
	}
	order, _ := buildWorkCreditOrder(groups, credits)

	return connect.NewResponse(&managev1.GetWorkCreditsResponse{
		Groups:  protoGroups,
		Credits: protoCredits,
		Order:   protoWorkCreditOrder(order),
	}), nil
}

// MoveWorkCreditItem applies one stable move intent against the latest Work
// credit order, so concurrent moves to different items compose under the Work
// aggregate lock.
func (s *WorkService) MoveWorkCreditItem(
	ctx context.Context,
	req *connect.Request[managev1.MoveWorkCreditItemRequest],
) (*connect.Response[managev1.MoveWorkCreditItemResponse], error) {
	if strings.TrimSpace(req.Msg.ItemId) == "" {
		return nil, errs.InvalidArgument("item_id", "cannot be empty")
	}
	switch req.Msg.Kind {
	case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP:
		if req.Msg.TargetGroupId != nil {
			return nil, errs.InvalidArgument("target_group_id", "must be omitted for a group move")
		}
	case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT:
		if req.Msg.TargetGroupId == nil {
			return nil, errs.InvalidArgument("target_group_id", "is required for a credit move")
		}
	default:
		return nil, errs.InvalidArgument("kind", "must identify a group or credit")
	}
	if workCreditAnchorIsSelf(req.Msg.After, req.Msg.Kind, req.Msg.ItemId) ||
		workCreditAnchorIsSelf(req.Msg.Before, req.Msg.Kind, req.Msg.ItemId) {
		return nil, errs.InvalidArgument("anchor", "cannot reference the moved item")
	}

	var result *managev1.MoveWorkCreditItemResponse
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, req.Msg.WorkId); err != nil {
			return err
		}
		groups, credits, currentOrder, persistedOrderValid, err := loadWorkCreditRows(ctx, tx, req.Msg.WorkId)
		if err != nil {
			return err
		}

		var nextOrder []workCreditOrderEntry
		switch req.Msg.Kind {
		case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP:
			if !hasWorkCreditGroup(groups, req.Msg.ItemId) {
				return errs.NotFound("credit_group", req.Msg.ItemId)
			}
			if err := validateWorkCreditMoveAnchors(
				ctx, tx, req.Msg.WorkId, currentOrder, req.Msg.Kind, req.Msg.ItemId, nil, req.Msg.After, req.Msg.Before,
			); err != nil {
				return err
			}
			var changed bool
			nextOrder, changed = moveWorkCreditGroupOrder(currentOrder, req.Msg.ItemId, req.Msg.After, req.Msg.Before)
			if !changed && persistedOrderValid {
				result = &managev1.MoveWorkCreditItemResponse{Items: protoWorkCreditOrder(currentOrder)}
				return nil
			}
		case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT:
			if !hasWorkCredit(credits, req.Msg.ItemId) {
				return errs.NotFound("credit", req.Msg.ItemId)
			}
			var targetGroupID *string
			requestedGroupID := strings.TrimSpace(*req.Msg.TargetGroupId)
			if requestedGroupID != "" {
				if !hasWorkCreditGroup(groups, requestedGroupID) {
					return errs.NotFound("credit_group", requestedGroupID)
				}
				targetGroupID = &requestedGroupID
			}
			if err := validateWorkCreditMoveAnchors(
				ctx, tx, req.Msg.WorkId, currentOrder, req.Msg.Kind, req.Msg.ItemId, targetGroupID, req.Msg.After, req.Msg.Before,
			); err != nil {
				return err
			}
			var changed bool
			nextOrder, changed = moveWorkCreditOrder(currentOrder, req.Msg.ItemId, targetGroupID, req.Msg.After, req.Msg.Before)
			if !changed && persistedOrderValid {
				result = &managev1.MoveWorkCreditItemResponse{Items: protoWorkCreditOrder(currentOrder)}
				return nil
			}
		}

		if err := persistWorkCreditOrder(ctx, tx, req.Msg.WorkId, nextOrder); err != nil {
			return err
		}
		if err := s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(
				metadata,
				req.Msg.WorkId,
				req.Msg.ItemId,
				sharedtelemetry.AuditItemOperationUpdated,
			)
		}); err != nil {
			return err
		}
		result = &managev1.MoveWorkCreditItemResponse{Items: protoWorkCreditOrder(nextOrder), Changed: true}
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(result), nil
}

func workCreditAnchorIsSelf(anchor *managev1.WorkCreditOrderItem, kind managev1.WorkCreditItemKind, itemID string) bool {
	return anchor != nil && anchor.Kind == kind && anchor.Id == itemID
}

func hasWorkCreditGroup(groups []model.WorkCreditGroup, groupID string) bool {
	for _, group := range groups {
		if group.ID == groupID {
			return true
		}
	}
	return false
}

func hasWorkCredit(credits []model.WorkCredit, creditID string) bool {
	for _, credit := range credits {
		if credit.ID == creditID {
			return true
		}
	}
	return false
}

// =============================================================================
// Admin Methods (all works, requires admin role)
// =============================================================================

// ListWorksAdmin returns a paginated list of all works with stats
func (s *WorkService) CreateWorkCreditGroup(
	ctx context.Context,
	req *connect.Request[managev1.CreateWorkCreditGroupRequest],
) (*connect.Response[managev1.WorkCreditGroup], error) {
	// Verify work exists
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", req.Msg.WorkId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.WorkId)
		}
		return nil, errs.Internal(err)
	}

	name, err := validateWorkCreditGroupName(req.Msg.Name)
	if err != nil {
		return nil, err
	}

	group := model.WorkCreditGroup{
		WorkID: req.Msg.WorkId,
		Name:   name,
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, group.WorkID); err != nil {
			return err
		}
		_, _, order, _, err := loadWorkCreditRows(ctx, tx, group.WorkID)
		if err != nil {
			return err
		}
		group.SortOrder = 0
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&group).Error; err != nil {
			return err
		}
		order = append(order, workCreditOrderEntry{
			kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP,
			id:   group.ID,
		})
		if err := persistWorkCreditOrder(ctx, tx, group.WorkID, order); err != nil {
			return err
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(metadata, group.WorkID, group.ID, sharedtelemetry.AuditItemOperationCreated)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(s.toProtoCreditGroup(&group)), nil
}

// UpdateWorkCreditGroup updates a credit group
func (s *WorkService) UpdateWorkCreditGroup(
	ctx context.Context,
	req *connect.Request[managev1.UpdateWorkCreditGroupRequest],
) (*connect.Response[managev1.WorkCreditGroup], error) {
	// Get group to find work ID
	var group model.WorkCreditGroup
	if err := s.db.WithContext(ctx).First(&group, "id = ?", req.Msg.GroupId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("credit_group", req.Msg.GroupId)
		}
		return nil, errs.Internal(err)
	}

	if err := RequireExists(ctx, s.db, group.WorkID); err != nil {
		return nil, err
	}

	updates := structured.Fields{}
	if req.Msg.Name != nil {
		name, err := validateWorkCreditGroupName(*req.Msg.Name)
		if err != nil {
			return nil, err
		}
		updates["name"] = name
	}
	if name, ok := updates["name"].(string); ok && name == group.Name {
		delete(updates, "name")
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, group.WorkID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, "id = ? AND work_id = ?", group.ID, group.WorkID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("credit_group", group.ID)
			}
			return err
		}
		if name, ok := updates["name"].(string); ok && name == group.Name {
			delete(updates, "name")
		}
		if len(updates) == 0 {
			return nil
		}
		if err := tx.Model(&group).Updates(updates).Error; err != nil {
			return err
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(metadata, group.WorkID, group.ID, sharedtelemetry.AuditItemOperationUpdated)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if err := s.db.WithContext(ctx).First(&group, "id = ?", req.Msg.GroupId).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(s.toProtoCreditGroup(&group)), nil
}

// DeleteWorkCreditGroup deletes a credit group
func (s *WorkService) DeleteWorkCreditGroup(
	ctx context.Context,
	req *connect.Request[managev1.DeleteWorkCreditGroupRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	// Get group to find work ID
	var group model.WorkCreditGroup
	if err := s.db.WithContext(ctx).First(&group, "id = ?", req.Msg.GroupId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("credit_group", req.Msg.GroupId)
		}
		return nil, errs.Internal(err)
	}

	if err := RequireExists(ctx, s.db, group.WorkID); err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, group.WorkID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, "id = ? AND work_id = ?", group.ID, group.WorkID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("credit_group", group.ID)
			}
			return err
		}
		groups, credits, order, _, err := loadWorkCreditRows(ctx, tx, group.WorkID)
		if err != nil {
			return err
		}
		if !hasWorkCreditGroup(groups, group.ID) {
			return errs.NotFound("credit_group", group.ID)
		}
		for index := range order {
			if order[index].kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT &&
				order[index].groupID != nil && *order[index].groupID == group.ID {
				order[index].groupID = nil
			}
		}
		for _, credit := range credits {
			if credit.GroupID != nil && *credit.GroupID == group.ID {
				if err := tx.Model(&model.WorkCredit{}).
					Where("id = ? AND work_id = ?", credit.ID, group.WorkID).
					Update("group_id", nil).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Delete(&group).Error; err != nil {
			return err
		}
		order = removeWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, group.ID)
		if err := persistWorkCreditOrder(ctx, tx, group.WorkID, order); err != nil {
			return err
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(metadata, group.WorkID, group.ID, sharedtelemetry.AuditItemOperationDeleted)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// AddWorkCredit adds a credit to a work
func (s *WorkService) AddWorkCredit(
	ctx context.Context,
	req *connect.Request[managev1.AddWorkCreditRequest],
) (*connect.Response[managev1.WorkCredit], error) {
	// Verify work exists
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", req.Msg.WorkId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.WorkId)
		}
		return nil, errs.Internal(err)
	}

	credit := model.WorkCredit{WorkID: req.Msg.WorkId}

	if req.Msg.GroupId != nil {
		credit.GroupID = req.Msg.GroupId
	}
	if req.Msg.ArtistId != nil {
		credit.ArtistID = req.Msg.ArtistId
	}
	if req.Msg.MemberId != nil {
		credit.MemberID = req.Msg.MemberId
	}
	if req.Msg.Name != nil {
		credit.Name = req.Msg.Name
	}
	if req.Msg.CreditRole != nil {
		credit.CreditRole = req.Msg.CreditRole
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, credit.WorkID); err != nil {
			return err
		}
		_, _, order, _, err := loadWorkCreditRows(ctx, tx, credit.WorkID)
		if err != nil {
			return err
		}
		if credit.GroupID != nil {
			if err := validateCreditGroupOwnershipWithDB(ctx, tx, credit.WorkID, *credit.GroupID); err != nil {
				return err
			}
		}
		credit.SortOrder = 0
		if req.Msg.MemberId != nil {
			if err := authorizationtarget.LockReferences(ctx, tx, []authorizationtarget.Reference{{
				MemberID: *req.Msg.MemberId,
				Field:    "member_id",
			}}); err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&credit).Error; err != nil {
			return err
		}
		order = insertWorkCreditAtGroupEnd(order, workCreditOrderEntry{
			kind:    managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
			id:      credit.ID,
			groupID: cloneGroupID(credit.GroupID),
		})
		if err := persistWorkCreditOrder(ctx, tx, credit.WorkID, order); err != nil {
			return err
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(metadata, credit.WorkID, credit.ID, sharedtelemetry.AuditItemOperationCreated)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(s.toProtoCredit(ctx, &credit)), nil
}

// UpdateWorkCredit updates a credit
func (s *WorkService) UpdateWorkCredit(
	ctx context.Context,
	req *connect.Request[managev1.UpdateWorkCreditRequest],
) (*connect.Response[managev1.WorkCredit], error) {
	// Get credit to find work ID
	var credit model.WorkCredit
	if err := s.db.WithContext(ctx).First(&credit, "id = ?", req.Msg.CreditId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("credit", req.Msg.CreditId)
		}
		return nil, errs.Internal(err)
	}

	if err := RequireExists(ctx, s.db, credit.WorkID); err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, credit.WorkID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credit, "id = ? AND work_id = ?", credit.ID, credit.WorkID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("credit", credit.ID)
			}
			return err
		}

		_, _, order, _, err := loadWorkCreditRows(ctx, tx, credit.WorkID)
		if err != nil {
			return err
		}
		updates := structured.Fields{}
		var targetGroupID *string
		groupChanged := false
		if req.Msg.GroupId != nil {
			requestedGroupID := strings.TrimSpace(*req.Msg.GroupId)
			if requestedGroupID != "" {
				if err := validateCreditGroupOwnershipWithDB(ctx, tx, credit.WorkID, requestedGroupID); err != nil {
					return err
				}
				targetGroupID = &requestedGroupID
			}
			groupChanged = !sameOptionalString(credit.GroupID, targetGroupID)
			if groupChanged {
				updates["group_id"] = targetGroupID
			}
		}
		if req.Msg.CreditRole != nil && (credit.CreditRole == nil || *req.Msg.CreditRole != *credit.CreditRole) {
			updates["credit_role"] = *req.Msg.CreditRole
		}
		if len(updates) == 0 {
			return nil
		}
		if err := tx.Model(&credit).Updates(updates).Error; err != nil {
			return err
		}
		if groupChanged {
			order, _ = moveWorkCreditOrder(order, credit.ID, targetGroupID, nil, nil)
			if err := persistWorkCreditOrder(ctx, tx, credit.WorkID, order); err != nil {
				return err
			}
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(metadata, credit.WorkID, credit.ID, sharedtelemetry.AuditItemOperationUpdated)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	// Reload
	if err := s.db.WithContext(ctx).First(&credit, "id = ?", req.Msg.CreditId).Error; err != nil {
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(s.toProtoCredit(ctx, &credit)), nil
}

// DeleteWorkCredit deletes a credit
func (s *WorkService) DeleteWorkCredit(
	ctx context.Context,
	req *connect.Request[managev1.DeleteWorkCreditRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	// Get credit to find work ID
	var credit model.WorkCredit
	if err := s.db.WithContext(ctx).First(&credit, "id = ?", req.Msg.CreditId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("credit", req.Msg.CreditId)
		}
		return nil, errs.Internal(err)
	}

	if err := RequireExists(ctx, s.db, credit.WorkID); err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockWorkAdmin(ctx, tx, credit.WorkID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credit, "id = ? AND work_id = ?", credit.ID, credit.WorkID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("credit", credit.ID)
			}
			return err
		}
		_, _, order, _, err := loadWorkCreditRows(ctx, tx, credit.WorkID)
		if err != nil {
			return err
		}
		if err := tx.Delete(&credit).Error; err != nil {
			return err
		}
		order = removeWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, credit.ID)
		if err := persistWorkCreditOrder(ctx, tx, credit.WorkID, order); err != nil {
			return err
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkCreditAuditRecord(metadata, credit.WorkID, credit.ID, sharedtelemetry.AuditItemOperationDeleted)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// ListMyCreditedWorks returns work-credit rows where the current user is directly credited
// or credited via artists they manage.
func validateWorkCreditGroupName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errs.InvalidArgument("name", "cannot be empty")
	}
	if len(trimmed) > 100 {
		return "", errs.InvalidArgument("name", "must be at most 100 characters")
	}
	return trimmed, nil
}

func (s *WorkService) validateCreditGroupOwnership(ctx context.Context, workID, groupID string) error {
	return validateCreditGroupOwnershipWithDB(ctx, s.db, workID, groupID)
}

func validateCreditGroupOwnershipWithDB(ctx context.Context, db *gorm.DB, workID, groupID string) error {
	var count int64
	if err := db.WithContext(ctx).
		Model(&model.WorkCreditGroup{}).
		Where("id = ? AND work_id = ?", groupID, workID).
		Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count == 0 {
		return errs.InvalidArgument("group_id", "must belong to this work")
	}
	return nil
}

// toProtoCreditGroup converts a model.WorkCreditGroup to protobuf WorkCreditGroup
func (s *WorkService) toProtoCreditGroup(g *model.WorkCreditGroup) *managev1.WorkCreditGroup {
	return &managev1.WorkCreditGroup{
		Id:     g.ID,
		WorkId: g.WorkID,
		Name:   g.Name,
	}
}

// toProtoCredit converts a model.WorkCredit to protobuf WorkCredit
func (s *WorkService) toProtoCredit(ctx context.Context, c *model.WorkCredit) *managev1.WorkCredit {
	credit := &managev1.WorkCredit{
		Id: c.ID,
	}

	if c.GroupID != nil {
		credit.GroupId = c.GroupID
	}
	if c.Name != nil {
		credit.Name = c.Name
	}
	if c.CreditRole != nil {
		credit.CreditRole = c.CreditRole
	}

	// Get artist info if artist_id is set
	if c.ArtistID != nil {
		credit.Artist = s.loadWorkCreditArtist(ctx, *c.ArtistID)
	}

	if c.MemberID != nil {
		if s.members == nil {
			return credit
		}
		summaries, err := s.members.LoadMemberSummaries(ctx, []string{*c.MemberID})
		if err == nil {
			credit.Member = summaries[*c.MemberID]
		}
	}

	return credit
}

type workCreditArtistRow struct {
	ID          string
	Name        string
	Slug        *string
	ImageFileID *string `gorm:"column:image_file_id"`
}

func (s *WorkService) loadWorkCreditArtist(ctx context.Context, artistID string) *managev1.CreditArtist {
	var artist workCreditArtistRow
	err := s.db.WithContext(ctx).
		Table("artist").
		Select("artist.id, "+ArtistSourceTitleSQL("artist")+" AS name, artist.slug, artist_image.file_id AS image_file_id").
		Joins(`
			LEFT JOIN LATERAL (
				SELECT artist_file.file_id
				FROM artist_file
				WHERE artist_file.artist_id = artist.id
				ORDER BY artist_file.sort_order ASC, artist_file.created_at ASC
				LIMIT 1
			) artist_image ON TRUE
		`).
		Where("artist.id = ?", artistID).
		Scan(&artist).Error
	if err != nil || artist.ID == "" {
		return nil
	}
	creditArtist := &managev1.CreditArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug}
	s.assignWorkCreditArtistImage(ctx, creditArtist, artist)
	return creditArtist
}

func (s *WorkService) assignWorkCreditArtistImage(
	ctx context.Context,
	creditArtist *managev1.CreditArtist,
	artist workCreditArtistRow,
) {
	if artist.ImageFileID == nil {
		return
	}
	asset, err := s.runtime.ReadyPublicAssetRefForSourceFile(ctx, s.db, *artist.ImageFileID, "image")
	if err != nil {
		slog.Warn(
			"Failed to resolve credited artist image asset",
			"artistId", artist.ID, "fileId", *artist.ImageFileID, "error", err,
		)
		return
	}
	creditArtist.ImageAsset = asset
}

// getWorkClients returns the clients associated with a work

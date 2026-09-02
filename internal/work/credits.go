package work

import (
	"context"
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

	// Get groups
	var groups []model.WorkCreditGroup
	if err := s.db.WithContext(ctx).
		Where("work_id = ?", req.Msg.WorkId).
		Order("sort_order").
		Find(&groups).Error; err != nil {
		return nil, errs.Wrap(err)
	}

	protoGroups := make([]*managev1.WorkCreditGroup, len(groups))
	for i, g := range groups {
		protoGroups[i] = s.toProtoCreditGroup(&g)
	}

	credits, err := s.getWorkCreditsWithError(ctx, req.Msg.WorkId)
	if err != nil {
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.GetWorkCreditsResponse{
		Groups:  protoGroups,
		Credits: credits,
	}), nil
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
		var maxSort int
		if err := tx.Table("work_credit_group").Select("COALESCE(MAX(sort_order), 0)").Where("work_id = ?", group.WorkID).Scan(&maxSort).Error; err != nil {
			return err
		}
		group.SortOrder = maxSort + 1
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&group).Error; err != nil {
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
		if err := tx.Delete(&group).Error; err != nil {
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

	credit := model.WorkCredit{
		WorkID:    req.Msg.WorkId,
		SortOrder: 1,
	}

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
		if credit.GroupID != nil {
			if err := validateCreditGroupOwnershipWithDB(ctx, tx, credit.WorkID, *credit.GroupID); err != nil {
				return err
			}
		}
		var maxSort int
		if err := tx.Table("work_credit").Select("COALESCE(MAX(sort_order), 0)").Where("work_id = ?", credit.WorkID).Scan(&maxSort).Error; err != nil {
			return err
		}
		credit.SortOrder = maxSort + 1
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

	// Build updates
	updates := structured.Fields{}

	if req.Msg.GroupId != nil {
		group, err := s.resolveWorkCreditGroup(ctx, credit.WorkID, *req.Msg.GroupId)
		if err != nil {
			return nil, err
		}
		if group.clear {
			updates["group_id"] = nil
		} else {
			updates["group_id"] = group.id
		}
	}
	if req.Msg.CreditRole != nil {
		updates["credit_role"] = *req.Msg.CreditRole
	}
	if groupID, ok := updates["group_id"].(string); ok && credit.GroupID != nil && groupID == *credit.GroupID {
		delete(updates, "group_id")
	}
	if updates["group_id"] == nil && credit.GroupID == nil {
		delete(updates, "group_id")
	}
	if role, ok := updates["credit_role"].(string); ok && credit.CreditRole != nil && role == *credit.CreditRole {
		delete(updates, "credit_role")
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
		if groupID, ok := updates["group_id"].(string); ok {
			if err := validateCreditGroupOwnershipWithDB(ctx, tx, credit.WorkID, groupID); err != nil {
				return err
			}
			if credit.GroupID != nil && groupID == *credit.GroupID {
				delete(updates, "group_id")
			}
		}
		if updates["group_id"] == nil && credit.GroupID == nil {
			delete(updates, "group_id")
		}
		if role, ok := updates["credit_role"].(string); ok && credit.CreditRole != nil && role == *credit.CreditRole {
			delete(updates, "credit_role")
		}
		if len(updates) == 0 {
			return nil
		}
		if err := tx.Model(&credit).Updates(updates).Error; err != nil {
			return err
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

type workCreditGroupUpdate struct {
	id    string
	clear bool
}

func (s *WorkService) resolveWorkCreditGroup(ctx context.Context, workID, requested string) (workCreditGroupUpdate, error) {
	groupID := strings.TrimSpace(requested)
	if groupID == "" {
		return workCreditGroupUpdate{clear: true}, nil
	}
	if err := s.validateCreditGroupOwnership(ctx, workID, groupID); err != nil {
		return workCreditGroupUpdate{}, err
	}
	return workCreditGroupUpdate{id: groupID}, nil
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
		if err := tx.Delete(&credit).Error; err != nil {
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

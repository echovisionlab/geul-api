package work

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type GetWorkCreditsQuery struct{ WorkID string }

// MoveWorkCreditItemCommand moves one group or credit next to an anchor of
// the latest order. A credit move names its target group; an empty one
// moves it out of every group.
type MoveWorkCreditItemCommand struct {
	WorkID        string
	Kind          managev1.WorkCreditItemKind
	ItemID        string
	TargetGroupID *string
	After         *managev1.WorkCreditOrderItem
	Before        *managev1.WorkCreditOrderItem
}

type CreateWorkCreditGroupCommand struct{ WorkID, Name string }

// UpdateWorkCreditGroupCommand renames a credit group; a nil Name keeps it.
type UpdateWorkCreditGroupCommand struct {
	GroupID string
	Name    *string
}

type DeleteWorkCreditGroupCommand struct{ GroupID string }

// AddWorkCreditCommand credits an Artist, a Member or a plain name.
type AddWorkCreditCommand struct {
	WorkID     string
	GroupID    *string
	ArtistID   *string
	MemberID   *string
	Name       *string
	CreditRole *string
}

// UpdateWorkCreditCommand moves a credit to GroupID, an empty one leaving
// every group, and sets its role. A nil field is left alone.
type UpdateWorkCreditCommand struct {
	CreditID   string
	GroupID    *string
	CreditRole *string
}

type DeleteWorkCreditCommand struct{ CreditID string }

// GetWorkCredits returns Work attribution for the Site Admin management surface.
func (s *Service) GetWorkCredits(ctx context.Context, query GetWorkCreditsQuery) (*managev1.GetWorkCreditsResponse, error) {
	var work model.Work
	if err := s.db.WithContext(ctx).Where("id = ?", query.WorkID).First(&work).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("work", query.WorkID)
		}
		return nil, err
	}
	if err := requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.View, workAuthorizationRead); err != nil {
		return nil, err
	}
	var groups []model.WorkCreditGroup
	var credits []model.WorkCredit
	var order []workCreditOrderEntry
	if err := mutation.Read(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		groups, credits, order, err = loadWorkCreditRows(ctx, tx, query.WorkID)
		return err
	}); err != nil {
		return nil, err
	}
	protoGroups := make([]*managev1.WorkCreditGroup, len(groups))
	for i := range groups {
		protoGroups[i] = toProtoCreditGroup(&groups[i])
	}
	protoCredits := make([]*managev1.WorkCredit, len(credits))
	for i := range credits {
		protoCredits[i] = s.toProtoCredit(ctx, &credits[i])
	}
	return &managev1.GetWorkCreditsResponse{
		Groups:  protoGroups,
		Credits: protoCredits,
		Order:   protoWorkCreditOrder(order),
	}, nil
}

// workCreditAudit names the record of one credit group or credit change.
func workCreditAudit(workID, itemID string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewWorkCreditAuditRecord(metadata, workID, itemID, operation)
	})
}

// workCreditChange is the outcome of a credit mutation.
type workCreditChange struct {
	workID    string
	itemID    string
	operation sharedtelemetry.AuditItemOperation
	changed   bool
}

func (change workCreditChange) audit() mutation.Audit {
	if !change.changed {
		return mutation.Unchanged()
	}
	return workCreditAudit(change.workID, change.itemID, change.operation)
}

// runWorkCreditChange runs change under the Work's manage authority, the
// root locked first, and records its credit item.
func (s *Service) runWorkCreditChange(ctx context.Context, workID string, change func(context.Context, *gorm.DB) (workCreditChange, error)) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[workCreditChange]{
		Access: lockedWork(workID, policyv1.Work.Manage, nil),
		Change: change,
		Audit:  workCreditChange.audit,
	})
	return err
}

// MoveWorkCreditItem applies one stable move intent against the latest Work
// credit order, so concurrent moves to different items compose under the Work
// aggregate lock.
func (s *Service) MoveWorkCreditItem(ctx context.Context, command MoveWorkCreditItemCommand) (*managev1.MoveWorkCreditItemResponse, error) {
	if err := validateWorkCreditMove(command); err != nil {
		return nil, err
	}
	var result *managev1.MoveWorkCreditItemResponse
	err := s.runWorkCreditChange(ctx, command.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: command.WorkID, itemID: command.ItemID, operation: sharedtelemetry.AuditItemOperationUpdated}
		currentOrder, nextOrder, err := planWorkCreditMove(ctx, tx, command)
		if err != nil {
			return change, err
		}
		if nextOrder == nil {
			result = &managev1.MoveWorkCreditItemResponse{Items: protoWorkCreditOrder(currentOrder)}
			return change, nil
		}
		if err := persistWorkCreditOrder(ctx, tx, command.WorkID, nextOrder); err != nil {
			return change, err
		}
		result = &managev1.MoveWorkCreditItemResponse{Items: protoWorkCreditOrder(nextOrder), Changed: true}
		change.changed = true
		return change, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateWorkCreditMove(command MoveWorkCreditItemCommand) error {
	if strings.TrimSpace(command.ItemID) == "" {
		return domainerr.InvalidField("item_id", "cannot be empty")
	}
	switch command.Kind {
	case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP:
		if command.TargetGroupID != nil {
			return domainerr.InvalidField("target_group_id", "must be omitted for a group move")
		}
	case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT:
		if command.TargetGroupID == nil {
			return domainerr.InvalidField("target_group_id", "is required for a credit move")
		}
	default:
		return domainerr.InvalidField("kind", "must identify a group or credit")
	}
	if workCreditAnchorIsSelf(command.After, command.Kind, command.ItemID) ||
		workCreditAnchorIsSelf(command.Before, command.Kind, command.ItemID) {
		return domainerr.InvalidField("anchor", "cannot reference the moved item")
	}
	return nil
}

// planWorkCreditMove returns the latest order and the order after the move,
// nil when the move changes nothing.
func planWorkCreditMove(ctx context.Context, tx *gorm.DB, command MoveWorkCreditItemCommand) ([]workCreditOrderEntry, []workCreditOrderEntry, error) {
	groups, credits, currentOrder, err := loadWorkCreditRows(ctx, tx, command.WorkID)
	if err != nil {
		return nil, nil, err
	}
	var targetGroupID *string
	if command.Kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
		if !hasWorkCreditGroup(groups, command.ItemID) {
			return nil, nil, domainerr.ResourceNotFound("credit_group", command.ItemID)
		}
	} else {
		if !hasWorkCredit(credits, command.ItemID) {
			return nil, nil, domainerr.ResourceNotFound("credit", command.ItemID)
		}
		if requested := strings.TrimSpace(*command.TargetGroupID); requested != "" {
			if !hasWorkCreditGroup(groups, requested) {
				return nil, nil, domainerr.ResourceNotFound("credit_group", requested)
			}
			targetGroupID = &requested
		}
	}
	if err := validateWorkCreditMoveAnchors(
		ctx, tx, currentOrder, command.Kind, command.ItemID, targetGroupID, command.After, command.Before,
	); err != nil {
		return nil, nil, err
	}
	var nextOrder []workCreditOrderEntry
	var changed bool
	if command.Kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
		nextOrder, changed = moveWorkCreditGroupOrder(currentOrder, command.ItemID, command.After, command.Before)
	} else {
		nextOrder, changed = moveWorkCreditOrder(currentOrder, command.ItemID, targetGroupID, command.After, command.Before)
	}
	if !changed {
		return currentOrder, nil, nil
	}
	return currentOrder, nextOrder, nil
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

// CreateWorkCreditGroup appends a credit group to a Work.
func (s *Service) CreateWorkCreditGroup(ctx context.Context, command CreateWorkCreditGroupCommand) (*managev1.WorkCreditGroup, error) {
	if err := requireStoredWork(ctx, s.db, command.WorkID); err != nil {
		return nil, err
	}
	name, err := validateWorkCreditGroupName(command.Name)
	if err != nil {
		return nil, err
	}
	group := model.WorkCreditGroup{WorkID: command.WorkID, Name: name}
	err = s.runWorkCreditChange(ctx, group.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: group.WorkID, operation: sharedtelemetry.AuditItemOperationCreated}
		_, _, order, err := loadWorkCreditRows(ctx, tx, group.WorkID)
		if err != nil {
			return change, err
		}
		group.SortOrder = 0
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&group).Error; err != nil {
			return change, err
		}
		order = append(order, workCreditOrderEntry{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, id: group.ID})
		change.itemID, change.changed = group.ID, true
		return change, persistWorkCreditOrder(ctx, tx, group.WorkID, order)
	})
	if err != nil {
		return nil, err
	}
	return toProtoCreditGroup(&group), nil
}

// loadWorkCreditGroup reads a credit group and answers a missing one or its
// missing Work before the mutation locks them.
func loadWorkCreditGroup(ctx context.Context, db *gorm.DB, groupID string) (model.WorkCreditGroup, error) {
	var group model.WorkCreditGroup
	if err := db.WithContext(ctx).First(&group, "id = ?", groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return group, domainerr.ResourceNotFound("credit_group", groupID)
		}
		return group, err
	}
	return group, RequireExists(ctx, db, group.WorkID)
}

// lockWorkCreditGroup locks the group of a locked Work.
func lockWorkCreditGroup(tx *gorm.DB, group *model.WorkCreditGroup) error {
	err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(group, "id = ? AND work_id = ?", group.ID, group.WorkID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("credit_group", group.ID)
	}
	return err
}

// UpdateWorkCreditGroup renames a credit group.
func (s *Service) UpdateWorkCreditGroup(ctx context.Context, command UpdateWorkCreditGroupCommand) (*managev1.WorkCreditGroup, error) {
	group, err := loadWorkCreditGroup(ctx, s.db, command.GroupID)
	if err != nil {
		return nil, err
	}
	updates := structured.Fields{}
	if command.Name != nil {
		name, err := validateWorkCreditGroupName(*command.Name)
		if err != nil {
			return nil, err
		}
		updates["name"] = name
	}
	err = s.runWorkCreditChange(ctx, group.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: group.WorkID, itemID: group.ID, operation: sharedtelemetry.AuditItemOperationUpdated}
		if err := lockWorkCreditGroup(tx, &group); err != nil {
			return change, err
		}
		if name, ok := updates["name"].(string); ok && name == group.Name {
			delete(updates, "name")
		}
		if len(updates) == 0 {
			return change, nil
		}
		change.changed = true
		return change, tx.Model(&group).Updates(updates).Error
	})
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&group, "id = ?", command.GroupID).Error; err != nil {
		return nil, err
	}
	return toProtoCreditGroup(&group), nil
}

// DeleteWorkCreditGroup deletes a credit group; its credits leave every
// group in place.
func (s *Service) DeleteWorkCreditGroup(ctx context.Context, command DeleteWorkCreditGroupCommand) error {
	group, err := loadWorkCreditGroup(ctx, s.db, command.GroupID)
	if err != nil {
		return err
	}
	return s.runWorkCreditChange(ctx, group.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: group.WorkID, itemID: group.ID, operation: sharedtelemetry.AuditItemOperationDeleted}
		if err := lockWorkCreditGroup(tx, &group); err != nil {
			return change, err
		}
		groups, credits, order, err := loadWorkCreditRows(ctx, tx, group.WorkID)
		if err != nil {
			return change, err
		}
		if !hasWorkCreditGroup(groups, group.ID) {
			return change, domainerr.ResourceNotFound("credit_group", group.ID)
		}
		if err := ungroupWorkCredits(tx, group, credits, order); err != nil {
			return change, err
		}
		if err := tx.Delete(&group).Error; err != nil {
			return change, err
		}
		order = removeWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, group.ID)
		change.changed = true
		return change, persistWorkCreditOrder(ctx, tx, group.WorkID, order)
	})
}

// ungroupWorkCredits moves the credits of group out of every group, in
// order and in their rows.
func ungroupWorkCredits(tx *gorm.DB, group model.WorkCreditGroup, credits []model.WorkCredit, order []workCreditOrderEntry) error {
	for index := range order {
		if order[index].kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT &&
			order[index].groupID != nil && *order[index].groupID == group.ID {
			order[index].groupID = nil
		}
	}
	for _, credit := range credits {
		if credit.GroupID == nil || *credit.GroupID != group.ID {
			continue
		}
		if err := tx.Model(&model.WorkCredit{}).
			Where("id = ? AND work_id = ?", credit.ID, group.WorkID).
			Update("group_id", nil).Error; err != nil {
			return err
		}
	}
	return nil
}

// AddWorkCredit adds a credit at the end of its group.
func (s *Service) AddWorkCredit(ctx context.Context, command AddWorkCreditCommand) (*managev1.WorkCredit, error) {
	if err := requireStoredWork(ctx, s.db, command.WorkID); err != nil {
		return nil, err
	}
	credit := model.WorkCredit{
		WorkID: command.WorkID, GroupID: command.GroupID, ArtistID: command.ArtistID,
		MemberID: command.MemberID, Name: command.Name, CreditRole: command.CreditRole,
	}
	err := s.runWorkCreditChange(ctx, credit.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: credit.WorkID, operation: sharedtelemetry.AuditItemOperationCreated}
		_, _, order, err := loadWorkCreditRows(ctx, tx, credit.WorkID)
		if err != nil {
			return change, err
		}
		if credit.GroupID != nil {
			if err := validateCreditGroupOwnershipWithDB(ctx, tx, credit.WorkID, *credit.GroupID); err != nil {
				return change, err
			}
		}
		credit.SortOrder = 0
		if command.MemberID != nil {
			if err := authorizationtarget.LockReferences(ctx, tx, []authorizationtarget.Reference{{
				MemberID: *command.MemberID,
				Field:    "member_id",
			}}); err != nil {
				return change, err
			}
		}
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&credit).Error; err != nil {
			return change, err
		}
		order = insertWorkCreditAtGroupEnd(order, workCreditOrderEntry{
			kind:    managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
			id:      credit.ID,
			groupID: cloneGroupID(credit.GroupID),
		})
		change.itemID, change.changed = credit.ID, true
		return change, persistWorkCreditOrder(ctx, tx, credit.WorkID, order)
	})
	if err != nil {
		return nil, err
	}
	return s.toProtoCredit(ctx, &credit), nil
}

// loadWorkCredit reads a credit and answers a missing one or its missing
// Work before the mutation locks them.
func loadWorkCredit(ctx context.Context, db *gorm.DB, creditID string) (model.WorkCredit, error) {
	var credit model.WorkCredit
	if err := db.WithContext(ctx).First(&credit, "id = ?", creditID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return credit, domainerr.ResourceNotFound("credit", creditID)
		}
		return credit, err
	}
	return credit, RequireExists(ctx, db, credit.WorkID)
}

// lockWorkCredit locks the credit of a locked Work.
func lockWorkCredit(tx *gorm.DB, credit *model.WorkCredit) error {
	err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(credit, "id = ? AND work_id = ?", credit.ID, credit.WorkID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("credit", credit.ID)
	}
	return err
}

// UpdateWorkCredit moves a credit to another group or sets its role.
func (s *Service) UpdateWorkCredit(ctx context.Context, command UpdateWorkCreditCommand) (*managev1.WorkCredit, error) {
	credit, err := loadWorkCredit(ctx, s.db, command.CreditID)
	if err != nil {
		return nil, err
	}
	err = s.runWorkCreditChange(ctx, credit.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: credit.WorkID, itemID: credit.ID, operation: sharedtelemetry.AuditItemOperationUpdated}
		if err := lockWorkCredit(tx, &credit); err != nil {
			return change, err
		}
		_, _, order, err := loadWorkCreditRows(ctx, tx, credit.WorkID)
		if err != nil {
			return change, err
		}
		updates, targetGroupID, groupChanged, err := planWorkCreditUpdate(ctx, tx, credit, command)
		if err != nil || len(updates) == 0 {
			return change, err
		}
		if err := tx.Model(&credit).Updates(updates).Error; err != nil {
			return change, err
		}
		change.changed = true
		if !groupChanged {
			return change, nil
		}
		order, _ = moveWorkCreditOrder(order, credit.ID, targetGroupID, nil, nil)
		return change, persistWorkCreditOrder(ctx, tx, credit.WorkID, order)
	})
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&credit, "id = ?", command.CreditID).Error; err != nil {
		return nil, err
	}
	return s.toProtoCredit(ctx, &credit), nil
}

// planWorkCreditUpdate returns the column updates of command against the
// locked credit, its target group and whether the group changes.
func planWorkCreditUpdate(ctx context.Context, tx *gorm.DB, credit model.WorkCredit, command UpdateWorkCreditCommand) (structured.Fields, *string, bool, error) {
	updates := structured.Fields{}
	var targetGroupID *string
	groupChanged := false
	if command.GroupID != nil {
		if requested := strings.TrimSpace(*command.GroupID); requested != "" {
			if err := validateCreditGroupOwnershipWithDB(ctx, tx, credit.WorkID, requested); err != nil {
				return nil, nil, false, err
			}
			targetGroupID = &requested
		}
		groupChanged = !sameOptionalString(credit.GroupID, targetGroupID)
		if groupChanged {
			updates["group_id"] = targetGroupID
		}
	}
	if command.CreditRole != nil && (credit.CreditRole == nil || *command.CreditRole != *credit.CreditRole) {
		updates["credit_role"] = *command.CreditRole
	}
	return updates, targetGroupID, groupChanged, nil
}

// DeleteWorkCredit deletes a credit.
func (s *Service) DeleteWorkCredit(ctx context.Context, command DeleteWorkCreditCommand) error {
	credit, err := loadWorkCredit(ctx, s.db, command.CreditID)
	if err != nil {
		return err
	}
	return s.runWorkCreditChange(ctx, credit.WorkID, func(ctx context.Context, tx *gorm.DB) (workCreditChange, error) {
		change := workCreditChange{workID: credit.WorkID, itemID: credit.ID, operation: sharedtelemetry.AuditItemOperationDeleted}
		if err := lockWorkCredit(tx, &credit); err != nil {
			return change, err
		}
		_, _, order, err := loadWorkCreditRows(ctx, tx, credit.WorkID)
		if err != nil {
			return change, err
		}
		if err := tx.Delete(&credit).Error; err != nil {
			return change, err
		}
		order = removeWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, credit.ID)
		change.changed = true
		return change, persistWorkCreditOrder(ctx, tx, credit.WorkID, order)
	})
}

func validateWorkCreditGroupName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", domainerr.InvalidField("name", "cannot be empty")
	}
	if len(trimmed) > 100 {
		return "", domainerr.InvalidField("name", "must be at most 100 characters")
	}
	return trimmed, nil
}

func validateCreditGroupOwnershipWithDB(ctx context.Context, db *gorm.DB, workID, groupID string) error {
	var count int64
	if err := db.WithContext(ctx).
		Model(&model.WorkCreditGroup{}).
		Where("id = ? AND work_id = ?", groupID, workID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return domainerr.InvalidField("group_id", "must belong to this work")
	}
	return nil
}

// toProtoCreditGroup converts a model.WorkCreditGroup to protobuf WorkCreditGroup
func toProtoCreditGroup(g *model.WorkCreditGroup) *managev1.WorkCreditGroup {
	return &managev1.WorkCreditGroup{
		Id:     g.ID,
		WorkId: g.WorkID,
		Name:   g.Name,
	}
}

// toProtoCredit converts a model.WorkCredit to protobuf WorkCredit
func (s *Service) toProtoCredit(ctx context.Context, c *model.WorkCredit) *managev1.WorkCredit {
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
		summaries, err := s.members.LoadMemberSummaries(ctx, []string{*c.MemberID})
		if err == nil {
			credit.Member = summaries[*c.MemberID]
		} else {
			slog.ErrorContext(ctx, "Work credit member lookup failed; serving credit without member summary", "domain", "work", "operation", "load_credit_member_summaries", failurecause.Attr(err), "work_id", c.WorkID, "credit_id", c.ID)
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

func (s *Service) loadWorkCreditArtist(ctx context.Context, artistID string) *managev1.CreditArtist {
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
	if err != nil {
		slog.ErrorContext(ctx, "Work credit artist lookup failed; serving credit without artist", "domain", "work", "operation", "load_credit_artist", failurecause.Attr(err), "artist_id", artistID)
		return nil
	}
	if artist.ID == "" {
		return nil
	}
	creditArtist := &managev1.CreditArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug}
	s.assignWorkCreditArtistImage(ctx, creditArtist, artist)
	return creditArtist
}

func (s *Service) assignWorkCreditArtistImage(
	ctx context.Context,
	creditArtist *managev1.CreditArtist,
	artist workCreditArtistRow,
) {
	if artist.ImageFileID == nil {
		return
	}
	asset, err := s.runtime.ReadyPublicAssetRefForSourceFile(ctx, s.db, *artist.ImageFileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "Work credit artist image resolution failed; serving artist without image", "domain", "work", "operation", "resolve_artist_image_asset", failurecause.Attr(err), "artist_id", artist.ID, "file_id", *artist.ImageFileID)
		return
	}
	creditArtist.ImageAsset = asset
}

// getWorkClients returns the clients associated with a work

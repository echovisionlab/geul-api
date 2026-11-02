package work

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type GetWorkCreditsQuery struct{ WorkID string }

// MoveWorkCreditItemCommand moves one group or credit next to an anchor of
// the latest order. A credit move names its target group; an empty one
// moves it out of every group.
type MoveWorkCreditItemCommand struct {
	WorkID        string
	Kind          CreditItemKind
	ItemID        string
	TargetGroupID *string
	After         *OrderItem
	Before        *OrderItem
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

// CreditArtist is a credited Artist with its ready image.
type CreditArtist struct {
	ID         string
	Name       string
	Slug       *string
	ImageAsset *mediaasset.Reference
}

// CreditView is a credit with its Artist and Member, when they resolve.
type CreditView struct {
	Credit Credit
	Artist *CreditArtist
	Member *memberref.Summary
}

// Credits is a Work's attribution: its groups, credits and their order.
type Credits struct {
	Groups  []CreditGroup
	Credits []CreditView
	Order   []OrderItem
}

// CreditMove is the credit order after a move.
type CreditMove struct {
	Items   []OrderItem
	Changed bool
}

// GetWorkCredits returns Work attribution for the Site Admin management surface.
func (s *Service) GetWorkCredits(ctx context.Context, query GetWorkCreditsQuery) (Credits, error) {
	work, err := s.findWork(ctx, query.WorkID)
	if err != nil {
		return Credits{}, err
	}
	if err := requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.View, workAuthorizationRead); err != nil {
		return Credits{}, err
	}
	var groups []CreditGroup
	var credits []Credit
	var order []workCreditOrderEntry
	if err := s.read(ctx, func(store Store) error {
		var err error
		groups, credits, order, err = loadWorkCreditRows(ctx, store, query.WorkID)
		return err
	}); err != nil {
		return Credits{}, err
	}
	views := make([]CreditView, len(credits))
	for i := range credits {
		views[i] = s.creditView(ctx, credits[i])
	}
	return Credits{Groups: groups, Credits: views, Order: workCreditOrderItems(order)}, nil
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
func (s *Service) runWorkCreditChange(ctx context.Context, workID string, change func(context.Context, Store) (workCreditChange, error)) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[workCreditChange, Store]{
		Access: lockedWork(workID, policyv1.Work.Manage, nil),
		Change: change,
		Audit:  workCreditChange.audit,
	})
	return err
}

// MoveWorkCreditItem applies one stable move intent against the latest Work
// credit order, so concurrent moves to different items compose under the Work
// aggregate lock.
func (s *Service) MoveWorkCreditItem(ctx context.Context, command MoveWorkCreditItemCommand) (CreditMove, error) {
	if err := validateWorkCreditMove(command); err != nil {
		return CreditMove{}, err
	}
	var result CreditMove
	err := s.runWorkCreditChange(ctx, command.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: command.WorkID, itemID: command.ItemID, operation: sharedtelemetry.AuditItemOperationUpdated}
		currentOrder, nextOrder, err := planWorkCreditMove(ctx, store, command)
		if err != nil {
			return change, err
		}
		if nextOrder == nil {
			result = CreditMove{Items: workCreditOrderItems(currentOrder)}
			return change, nil
		}
		if err := persistWorkCreditOrder(ctx, store, command.WorkID, nextOrder); err != nil {
			return change, err
		}
		result = CreditMove{Items: workCreditOrderItems(nextOrder), Changed: true}
		change.changed = true
		return change, nil
	})
	if err != nil {
		return CreditMove{}, err
	}
	return result, nil
}

func validateWorkCreditMove(command MoveWorkCreditItemCommand) error {
	if strings.TrimSpace(command.ItemID) == "" {
		return domainerr.InvalidField("item_id", "cannot be empty")
	}
	switch command.Kind {
	case CreditItemGroup:
		if command.TargetGroupID != nil {
			return domainerr.InvalidField("target_group_id", "must be omitted for a group move")
		}
	case CreditItemCredit:
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
func planWorkCreditMove(ctx context.Context, store Store, command MoveWorkCreditItemCommand) ([]workCreditOrderEntry, []workCreditOrderEntry, error) {
	groups, credits, currentOrder, err := loadWorkCreditRows(ctx, store, command.WorkID)
	if err != nil {
		return nil, nil, err
	}
	var targetGroupID *string
	if command.Kind == CreditItemGroup {
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
		ctx, store, currentOrder, command.Kind, command.ItemID, targetGroupID, command.After, command.Before,
	); err != nil {
		return nil, nil, err
	}
	var nextOrder []workCreditOrderEntry
	var changed bool
	if command.Kind == CreditItemGroup {
		nextOrder, changed = moveWorkCreditGroupOrder(currentOrder, command.ItemID, command.After, command.Before)
	} else {
		nextOrder, changed = moveWorkCreditOrder(currentOrder, command.ItemID, targetGroupID, command.After, command.Before)
	}
	if !changed {
		return currentOrder, nil, nil
	}
	return currentOrder, nextOrder, nil
}

func workCreditAnchorIsSelf(anchor *OrderItem, kind CreditItemKind, itemID string) bool {
	return anchor != nil && anchor.Kind == kind && anchor.ID == itemID
}

func hasWorkCreditGroup(groups []CreditGroup, groupID string) bool {
	for _, group := range groups {
		if group.ID == groupID {
			return true
		}
	}
	return false
}

func hasWorkCredit(credits []Credit, creditID string) bool {
	for _, credit := range credits {
		if credit.ID == creditID {
			return true
		}
	}
	return false
}

// CreateWorkCreditGroup appends a credit group to a Work.
func (s *Service) CreateWorkCreditGroup(ctx context.Context, command CreateWorkCreditGroupCommand) (CreditGroup, error) {
	if err := s.requireStoredWork(ctx, command.WorkID); err != nil {
		return CreditGroup{}, err
	}
	name, err := validateWorkCreditGroupName(command.Name)
	if err != nil {
		return CreditGroup{}, err
	}
	group := CreditGroup{WorkID: command.WorkID, Name: name}
	err = s.runWorkCreditChange(ctx, group.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: group.WorkID, operation: sharedtelemetry.AuditItemOperationCreated}
		_, _, order, err := loadWorkCreditRows(ctx, store, group.WorkID)
		if err != nil {
			return change, err
		}
		group.SortOrder = 0
		if err := store.InsertCreditGroup(ctx, &group); err != nil {
			return change, err
		}
		order = append(order, workCreditOrderEntry{kind: CreditItemGroup, id: group.ID})
		change.itemID, change.changed = group.ID, true
		return change, persistWorkCreditOrder(ctx, store, group.WorkID, order)
	})
	if err != nil {
		return CreditGroup{}, err
	}
	return group, nil
}

// loadWorkCreditGroup reads a credit group and answers a missing one or its
// missing Work before the mutation locks them.
func (s *Service) loadWorkCreditGroup(ctx context.Context, groupID string) (CreditGroup, error) {
	var group CreditGroup
	err := s.read(ctx, func(store Store) error {
		var err error
		group, err = store.FindCreditGroup(ctx, groupID)
		if errors.Is(err, ErrCreditGroupNotFound) {
			return domainerr.ResourceNotFound("credit_group", groupID)
		}
		if err != nil {
			return err
		}
		return requireWorkExists(ctx, store, group.WorkID)
	})
	return group, err
}

// lockWorkCreditGroup locks the group of a locked Work.
func lockWorkCreditGroup(ctx context.Context, store Store, group *CreditGroup) error {
	locked, err := store.LockCreditGroup(ctx, group.ID, group.WorkID)
	if errors.Is(err, ErrCreditGroupNotFound) {
		return domainerr.ResourceNotFound("credit_group", group.ID)
	}
	if err != nil {
		return err
	}
	*group = locked
	return nil
}

// UpdateWorkCreditGroup renames a credit group.
func (s *Service) UpdateWorkCreditGroup(ctx context.Context, command UpdateWorkCreditGroupCommand) (CreditGroup, error) {
	group, err := s.loadWorkCreditGroup(ctx, command.GroupID)
	if err != nil {
		return CreditGroup{}, err
	}
	var rename *string
	if command.Name != nil {
		name, err := validateWorkCreditGroupName(*command.Name)
		if err != nil {
			return CreditGroup{}, err
		}
		rename = &name
	}
	err = s.runWorkCreditChange(ctx, group.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: group.WorkID, itemID: group.ID, operation: sharedtelemetry.AuditItemOperationUpdated}
		if err := lockWorkCreditGroup(ctx, store, &group); err != nil {
			return change, err
		}
		if rename == nil || *rename == group.Name {
			return change, nil
		}
		change.changed = true
		return change, store.RenameCreditGroup(ctx, group.ID, *rename)
	})
	if err != nil {
		return CreditGroup{}, err
	}
	if err := s.read(ctx, func(store Store) error {
		var err error
		group, err = store.FindCreditGroup(ctx, command.GroupID)
		return err
	}); err != nil {
		return CreditGroup{}, err
	}
	return group, nil
}

// DeleteWorkCreditGroup deletes a credit group; its credits leave every
// group in place.
func (s *Service) DeleteWorkCreditGroup(ctx context.Context, command DeleteWorkCreditGroupCommand) error {
	group, err := s.loadWorkCreditGroup(ctx, command.GroupID)
	if err != nil {
		return err
	}
	return s.runWorkCreditChange(ctx, group.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: group.WorkID, itemID: group.ID, operation: sharedtelemetry.AuditItemOperationDeleted}
		if err := lockWorkCreditGroup(ctx, store, &group); err != nil {
			return change, err
		}
		groups, credits, order, err := loadWorkCreditRows(ctx, store, group.WorkID)
		if err != nil {
			return change, err
		}
		if !hasWorkCreditGroup(groups, group.ID) {
			return change, domainerr.ResourceNotFound("credit_group", group.ID)
		}
		if err := ungroupWorkCredits(ctx, store, group, credits, order); err != nil {
			return change, err
		}
		if err := store.DeleteCreditGroup(ctx, group.ID); err != nil {
			return change, err
		}
		order = removeWorkCreditOrderEntry(order, CreditItemGroup, group.ID)
		change.changed = true
		return change, persistWorkCreditOrder(ctx, store, group.WorkID, order)
	})
}

// ungroupWorkCredits moves the credits of group out of every group, in
// order and in their rows.
func ungroupWorkCredits(ctx context.Context, store Store, group CreditGroup, credits []Credit, order []workCreditOrderEntry) error {
	for index := range order {
		if order[index].kind == CreditItemCredit &&
			order[index].groupID != nil && *order[index].groupID == group.ID {
			order[index].groupID = nil
		}
	}
	for _, credit := range credits {
		if credit.GroupID == nil || *credit.GroupID != group.ID {
			continue
		}
		if err := store.UngroupCredit(ctx, credit.ID, group.WorkID); err != nil {
			return err
		}
	}
	return nil
}

// AddWorkCredit adds a credit at the end of its group.
func (s *Service) AddWorkCredit(ctx context.Context, command AddWorkCreditCommand) (CreditView, error) {
	if err := s.requireStoredWork(ctx, command.WorkID); err != nil {
		return CreditView{}, err
	}
	credit := Credit{
		WorkID: command.WorkID, GroupID: command.GroupID, ArtistID: command.ArtistID,
		MemberID: command.MemberID, Name: command.Name, CreditRole: command.CreditRole,
	}
	err := s.runWorkCreditChange(ctx, credit.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: credit.WorkID, operation: sharedtelemetry.AuditItemOperationCreated}
		_, _, order, err := loadWorkCreditRows(ctx, store, credit.WorkID)
		if err != nil {
			return change, err
		}
		if credit.GroupID != nil {
			if err := validateCreditGroupOwnership(ctx, store, credit.WorkID, *credit.GroupID); err != nil {
				return change, err
			}
		}
		credit.SortOrder = 0
		if command.MemberID != nil {
			if err := store.LockCreditMember(ctx, *command.MemberID); err != nil {
				return change, err
			}
		}
		if err := store.InsertCredit(ctx, &credit); err != nil {
			return change, err
		}
		order = insertWorkCreditAtGroupEnd(order, workCreditOrderEntry{
			kind:    CreditItemCredit,
			id:      credit.ID,
			groupID: cloneGroupID(credit.GroupID),
		})
		change.itemID, change.changed = credit.ID, true
		return change, persistWorkCreditOrder(ctx, store, credit.WorkID, order)
	})
	if err != nil {
		return CreditView{}, err
	}
	return s.creditView(ctx, credit), nil
}

// loadWorkCredit reads a credit and answers a missing one or its missing
// Work before the mutation locks them.
func (s *Service) loadWorkCredit(ctx context.Context, creditID string) (Credit, error) {
	var credit Credit
	err := s.read(ctx, func(store Store) error {
		var err error
		credit, err = store.FindCredit(ctx, creditID)
		if errors.Is(err, ErrCreditNotFound) {
			return domainerr.ResourceNotFound("credit", creditID)
		}
		if err != nil {
			return err
		}
		return requireWorkExists(ctx, store, credit.WorkID)
	})
	return credit, err
}

// lockWorkCredit locks the credit of a locked Work.
func lockWorkCredit(ctx context.Context, store Store, credit *Credit) error {
	locked, err := store.LockCredit(ctx, credit.ID, credit.WorkID)
	if errors.Is(err, ErrCreditNotFound) {
		return domainerr.ResourceNotFound("credit", credit.ID)
	}
	if err != nil {
		return err
	}
	*credit = locked
	return nil
}

// UpdateWorkCredit moves a credit to another group or sets its role.
func (s *Service) UpdateWorkCredit(ctx context.Context, command UpdateWorkCreditCommand) (CreditView, error) {
	credit, err := s.loadWorkCredit(ctx, command.CreditID)
	if err != nil {
		return CreditView{}, err
	}
	err = s.runWorkCreditChange(ctx, credit.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: credit.WorkID, itemID: credit.ID, operation: sharedtelemetry.AuditItemOperationUpdated}
		if err := lockWorkCredit(ctx, store, &credit); err != nil {
			return change, err
		}
		_, _, order, err := loadWorkCreditRows(ctx, store, credit.WorkID)
		if err != nil {
			return change, err
		}
		update, err := planWorkCreditUpdate(ctx, store, credit, command)
		if err != nil || !update.changed() {
			return change, err
		}
		if err := store.UpdateCredit(ctx, credit.ID, update.groupChanged, update.targetGroupID, update.role); err != nil {
			return change, err
		}
		change.changed = true
		if !update.groupChanged {
			return change, nil
		}
		order, _ = moveWorkCreditOrder(order, credit.ID, update.targetGroupID, nil, nil)
		return change, persistWorkCreditOrder(ctx, store, credit.WorkID, order)
	})
	if err != nil {
		return CreditView{}, err
	}
	if err := s.read(ctx, func(store Store) error {
		var err error
		credit, err = store.FindCredit(ctx, command.CreditID)
		return err
	}); err != nil {
		return CreditView{}, err
	}
	return s.creditView(ctx, credit), nil
}

// workCreditUpdate is what an update writes to the locked credit.
type workCreditUpdate struct {
	targetGroupID *string
	groupChanged  bool
	role          *string
}

func (update workCreditUpdate) changed() bool { return update.groupChanged || update.role != nil }

// planWorkCreditUpdate returns what command writes to the locked credit.
func planWorkCreditUpdate(ctx context.Context, store Store, credit Credit, command UpdateWorkCreditCommand) (workCreditUpdate, error) {
	var update workCreditUpdate
	if command.GroupID != nil {
		if requested := strings.TrimSpace(*command.GroupID); requested != "" {
			if err := validateCreditGroupOwnership(ctx, store, credit.WorkID, requested); err != nil {
				return workCreditUpdate{}, err
			}
			update.targetGroupID = &requested
		}
		update.groupChanged = !sameOptionalString(credit.GroupID, update.targetGroupID)
	}
	if command.CreditRole != nil && (credit.CreditRole == nil || *command.CreditRole != *credit.CreditRole) {
		role := *command.CreditRole
		update.role = &role
	}
	return update, nil
}

// DeleteWorkCredit deletes a credit.
func (s *Service) DeleteWorkCredit(ctx context.Context, command DeleteWorkCreditCommand) error {
	credit, err := s.loadWorkCredit(ctx, command.CreditID)
	if err != nil {
		return err
	}
	return s.runWorkCreditChange(ctx, credit.WorkID, func(ctx context.Context, store Store) (workCreditChange, error) {
		change := workCreditChange{workID: credit.WorkID, itemID: credit.ID, operation: sharedtelemetry.AuditItemOperationDeleted}
		if err := lockWorkCredit(ctx, store, &credit); err != nil {
			return change, err
		}
		_, _, order, err := loadWorkCreditRows(ctx, store, credit.WorkID)
		if err != nil {
			return change, err
		}
		if err := store.DeleteCredit(ctx, credit.ID); err != nil {
			return change, err
		}
		order = removeWorkCreditOrderEntry(order, CreditItemCredit, credit.ID)
		change.changed = true
		return change, persistWorkCreditOrder(ctx, store, credit.WorkID, order)
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

func validateCreditGroupOwnership(ctx context.Context, store Store, workID, groupID string) error {
	owned, err := store.CreditGroupInWork(ctx, workID, groupID)
	if err != nil {
		return err
	}
	if !owned {
		return domainerr.InvalidField("group_id", "must belong to this work")
	}
	return nil
}

// creditView resolves a credit's Artist and Member. A failed lookup is
// logged and leaves it unresolved.
func (s *Service) creditView(ctx context.Context, credit Credit) CreditView {
	view := CreditView{Credit: credit}
	if credit.ArtistID != nil {
		view.Artist = s.loadWorkCreditArtist(ctx, *credit.ArtistID)
	}
	if credit.MemberID != nil {
		summaries, err := s.members.LoadMemberSummaries(ctx, []string{*credit.MemberID})
		if err == nil {
			view.Member = summaries[*credit.MemberID]
		} else {
			slog.ErrorContext(ctx, "Work credit member lookup failed; serving credit without member summary", "domain", "work", "operation", "load_credit_member_summaries", failurecause.Attr(err), "work_id", credit.WorkID, "credit_id", credit.ID)
		}
	}
	return view
}

func (s *Service) loadWorkCreditArtist(ctx context.Context, artistID string) *CreditArtist {
	var artist CreditArtistRow
	var found bool
	if err := s.read(ctx, func(store Store) error {
		var err error
		artist, found, err = store.CreditArtist(ctx, artistID)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Work credit artist lookup failed; serving credit without artist", "domain", "work", "operation", "load_credit_artist", failurecause.Attr(err), "artist_id", artistID)
		return nil
	}
	if !found {
		return nil
	}
	creditArtist := &CreditArtist{ID: artist.ID, Name: artist.Name, Slug: artist.Slug}
	s.assignWorkCreditArtistImage(ctx, creditArtist, artist)
	return creditArtist
}

func (s *Service) assignWorkCreditArtistImage(ctx context.Context, creditArtist *CreditArtist, artist CreditArtistRow) {
	if artist.ImageFileID == nil {
		return
	}
	asset, err := s.readyFileAsset(ctx, *artist.ImageFileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "Work credit artist image resolution failed; serving artist without image", "domain", "work", "operation", "resolve_artist_image_asset", failurecause.Attr(err), "artist_id", artist.ID, "file_id", *artist.ImageFileID)
		return
	}
	creditArtist.ImageAsset = asset
}

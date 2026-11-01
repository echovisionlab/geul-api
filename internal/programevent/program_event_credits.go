package programevent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// AddProgramEventCreditCommand adds a credit; exactly one of ArtistID,
// MemberID and DisplayName names the credited party. A nil SortOrder puts the
// credit last.
type AddProgramEventCreditCommand struct {
	EventID     string
	ArtistID    *string
	MemberID    *string
	DisplayName *string
	CreditRole  *string
	Description *string
	SortOrder   *int32
}

// AddProgramEventCredit adds one credit to the event.
func (s *Service) AddProgramEventCredit(ctx context.Context, command AddProgramEventCreditCommand) (CreditView, error) {
	if strings.TrimSpace(command.EventID) == "" {
		return CreditView{}, domainerr.Required("event_id")
	}
	if countPresent(command.ArtistID, command.MemberID, command.DisplayName) != 1 {
		return CreditView{}, domainerr.InvalidField("credit", "exactly one of artist_id, user_id, display_name is required")
	}
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	credit, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[CreditRecord, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (CreditRecord, error) {
			return addProgramEventCredit(ctx, store, command)
		},
		Audit: func(credit CreditRecord) mutation.Audit {
			return childAudit(command.EventID, "credits", credit.ID, sharedtelemetry.AuditItemOperationCreated)
		},
	})
	if err != nil {
		return CreditView{}, err
	}
	return s.committedCreditView(ctx, credit), nil
}

func addProgramEventCredit(ctx context.Context, store Store, command AddProgramEventCreditCommand) (CreditRecord, error) {
	if memberID := strings.TrimSpace(derefString(command.MemberID)); memberID != "" {
		if err := store.LockCreditMembers(ctx, []CreditMemberReference{{
			MemberID: memberID,
			Field:    "member_id",
		}}); err != nil {
			return CreditRecord{}, err
		}
	}
	var sortOrder int32
	if command.SortOrder != nil {
		sortOrder = *command.SortOrder
	} else {
		next, err := store.NextCreditSortOrder(ctx, command.EventID)
		if err != nil {
			return CreditRecord{}, err
		}
		sortOrder = next
	}
	credit := CreditRecord{
		EventID:     command.EventID,
		ArtistID:    normalizedStringPtr(command.ArtistID),
		MemberID:    normalizedStringPtr(command.MemberID),
		DisplayName: normalizedStringPtr(command.DisplayName),
		CreditRole:  normalizedStringPtr(command.CreditRole),
		Description: normalizedStringPtr(command.Description),
		SortOrder:   sortOrder,
		CreatedAt:   dbtime.Now(),
	}
	if err := store.InsertCredit(ctx, &credit); err != nil {
		return CreditRecord{}, err
	}
	return credit, store.TouchEvent(ctx, command.EventID, time.Now().UTC())
}

// UpdateProgramEventCreditCommand changes a credit's role, description or
// position; nil fields are left alone.
type UpdateProgramEventCreditCommand struct {
	EventID     string
	CreditID    string
	CreditRole  *string
	Description *string
	SortOrder   *int32
}

type creditChange struct {
	credit  CreditRecord
	changed bool
}

// UpdateProgramEventCredit changes one credit of the event.
func (s *Service) UpdateProgramEventCredit(ctx context.Context, command UpdateProgramEventCreditCommand) (CreditView, error) {
	if strings.TrimSpace(command.EventID) == "" {
		return CreditView{}, domainerr.Required("event_id")
	}
	if strings.TrimSpace(command.CreditID) == "" {
		return CreditView{}, domainerr.Required("credit_id")
	}
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[creditChange, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (creditChange, error) {
			return updateProgramEventCredit(ctx, store, command)
		},
		Audit: func(change creditChange) mutation.Audit {
			if !change.changed {
				return mutation.Unchanged()
			}
			return childAudit(command.EventID, "credits", change.credit.ID, sharedtelemetry.AuditItemOperationUpdated)
		},
	})
	if err != nil {
		return CreditView{}, err
	}
	return s.committedCreditView(ctx, change.credit), nil
}

func updateProgramEventCredit(ctx context.Context, store Store, command UpdateProgramEventCreditCommand) (creditChange, error) {
	credit, err := loadProgramEventCredit(ctx, store, command.EventID, command.CreditID)
	if err != nil {
		return creditChange{}, err
	}
	update := CreditUpdate{SortOrder: command.SortOrder}
	if command.CreditRole != nil {
		update.CreditRole = setNullable(normalizedStringPtr(command.CreditRole))
	}
	if command.Description != nil {
		update.Description = setNullable(normalizedStringPtr(command.Description))
	}
	if !creditChanged(update, credit) {
		return creditChange{credit: credit}, nil
	}
	if err := store.UpdateCredit(ctx, credit.ID, update); err != nil {
		return creditChange{}, err
	}
	credit, err = store.Credit(ctx, command.EventID, command.CreditID)
	if err != nil {
		return creditChange{}, err
	}
	return creditChange{credit: credit, changed: true}, store.TouchEvent(ctx, command.EventID, time.Now().UTC())
}

func loadProgramEventCredit(ctx context.Context, store Store, eventID, creditID string) (CreditRecord, error) {
	credit, err := store.Credit(ctx, eventID, creditID)
	if errors.Is(err, ErrCreditNotFound) {
		return CreditRecord{}, domainerr.ResourceNotFound("program event credit", creditID)
	}
	return credit, err
}

// DeleteProgramEventCreditCommand names the credit to delete.
type DeleteProgramEventCreditCommand struct {
	EventID  string
	CreditID string
}

// DeleteProgramEventCredit deletes one credit of the event.
func (s *Service) DeleteProgramEventCredit(ctx context.Context, command DeleteProgramEventCreditCommand) error {
	if strings.TrimSpace(command.EventID) == "" {
		return domainerr.Required("event_id")
	}
	if strings.TrimSpace(command.CreditID) == "" {
		return domainerr.Required("credit_id")
	}
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (string, error) {
			credit, err := loadProgramEventCredit(ctx, store, command.EventID, command.CreditID)
			if err != nil {
				return "", err
			}
			deleted, err := store.DeleteCredit(ctx, command.EventID, command.CreditID)
			if err != nil {
				return "", err
			}
			if !deleted {
				return "", domainerr.ResourceNotFound("program event credit", command.CreditID)
			}
			return credit.ID, store.TouchEvent(ctx, command.EventID, time.Now().UTC())
		},
		Audit: func(creditID string) mutation.Audit {
			return childAudit(command.EventID, "credits", creditID, sharedtelemetry.AuditItemOperationDeleted)
		},
	})
	return err
}

// ReorderProgramEventCreditsCommand orders every credit of the event.
type ReorderProgramEventCreditsCommand struct {
	EventID   string
	CreditIDs []string
}

// CreditOrder is the outcome of a credit reorder.
type CreditOrder struct {
	Changed   bool
	UpdatedAt time.Time
}

// ReorderProgramEventCredits orders the credits of the event.
func (s *Service) ReorderProgramEventCredits(ctx context.Context, command ReorderProgramEventCreditsCommand) (CreditOrder, error) {
	if strings.TrimSpace(command.EventID) == "" {
		return CreditOrder{}, domainerr.Required("event_id")
	}
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[CreditOrder, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (CreditOrder, error) {
			return reorderProgramEventCredits(ctx, store, command)
		},
		Audit: func(order CreditOrder) mutation.Audit {
			if !order.Changed {
				return mutation.Unchanged()
			}
			return childOrderAudit(command.EventID, "credits", command.CreditIDs)
		},
	})
}

func reorderProgramEventCredits(ctx context.Context, store Store, command ReorderProgramEventCreditsCommand) (CreditOrder, error) {
	if err := validateProgramEventCreditOrder(ctx, store, command); err != nil {
		return CreditOrder{}, err
	}
	current, err := store.Credits(ctx, command.EventID)
	if err != nil {
		return CreditOrder{}, err
	}
	if len(current) != len(command.CreditIDs) {
		return CreditOrder{}, domainerr.InvalidField("credit_ids", "credit order must include every item")
	}
	changed := !sameStringSlice(creditIDs(current), command.CreditIDs)
	if changed {
		for index, creditID := range command.CreditIDs {
			if err := store.PlaceCredit(ctx, command.EventID, creditID, index); err != nil {
				return CreditOrder{}, err
			}
		}
	}
	updatedAt, err := programEventRelationMutationUpdatedAt(ctx, store, command.EventID, changed)
	return CreditOrder{Changed: changed, UpdatedAt: updatedAt}, err
}

// validateProgramEventCreditOrder requires distinct credit IDs that all
// belong to the event.
func validateProgramEventCreditOrder(ctx context.Context, store Store, command ReorderProgramEventCreditsCommand) error {
	seen := make(map[string]struct{}, len(command.CreditIDs))
	for _, creditID := range command.CreditIDs {
		if strings.TrimSpace(creditID) == "" {
			return domainerr.Required("credit_id")
		}
		if _, ok := seen[creditID]; ok {
			return domainerr.InvalidField("credit_ids", "duplicate credit id")
		}
		seen[creditID] = struct{}{}
	}
	if len(command.CreditIDs) == 0 {
		return nil
	}
	count, err := store.CountCredits(ctx, command.EventID, command.CreditIDs)
	if err != nil {
		return err
	}
	if count != int64(len(command.CreditIDs)) {
		return domainerr.InvalidField("credit_ids", "all credits must belong to the event")
	}
	return nil
}

func creditIDs(credits []CreditRecord) []string {
	ids := make([]string, len(credits))
	for index := range credits {
		ids[index] = credits[index].ID
	}
	return ids
}

// creditChanged reports whether update changes credit.
func creditChanged(update CreditUpdate, credit CreditRecord) bool {
	for _, column := range []struct {
		current *string
		update  Nullable[string]
	}{
		{credit.ArtistID, update.ArtistID},
		{credit.MemberID, update.MemberID},
		{credit.DisplayName, update.DisplayName},
		{credit.CreditRole, update.CreditRole},
		{credit.Description, update.Description},
	} {
		if column.update.Set && !sameNullableString(column.current, column.update.Value) {
			return true
		}
	}
	return update.SortOrder != nil && credit.SortOrder != *update.SortOrder
}

// replaceProgramEventRelations replaces every relation of kind with inputs;
// field names a missing related ID.
func replaceProgramEventRelations(ctx context.Context, store Store, kind RelationKind, field, eventID string, inputs []Relation) error {
	if err := store.DeleteRelations(ctx, kind, eventID); err != nil {
		return err
	}
	rows := make([]Relation, 0, len(inputs))
	now := time.Now().UTC()
	for _, input := range inputs {
		if input.ID == "" {
			return domainerr.Required(field)
		}
		rows = append(rows, input)
	}
	if len(rows) == 0 {
		return nil
	}
	return store.InsertRelations(ctx, kind, eventID, rows, now)
}

type programEventCreditReplacement struct {
	changes      []programEventCreditChange
	orderedIDs   []string
	orderChanged bool
}

type programEventCreditChange struct {
	id        string
	operation sharedtelemetry.AuditItemOperation
}

func replaceProgramEventCredits(ctx context.Context, store Store, eventID string, credits []Credit) (programEventCreditReplacement, error) {
	result := programEventCreditReplacement{}
	references := make([]CreditMemberReference, 0, len(credits))
	for i, credit := range credits {
		if countPresent(credit.ArtistID, credit.MemberID, credit.DisplayName) != 1 {
			return result, domainerr.InvalidField("credit", "exactly one of artist_id, user_id, display_name is required")
		}
		if credit.MemberID != nil {
			references = append(references, CreditMemberReference{
				MemberID: *credit.MemberID,
				Field:    fmt.Sprintf("credit[%d].member_id", i),
			})
		}
	}
	if err := store.LockCreditMembers(ctx, references); err != nil {
		return result, err
	}
	existing, err := store.LockCredits(ctx, eventID)
	if err != nil {
		return result, err
	}
	previousIDs := creditIDs(existing)
	byID := make(map[string]CreditRecord, len(existing))
	for index := range existing {
		byID[existing[index].ID] = existing[index]
	}
	seen := make(map[string]struct{}, len(credits))
	for _, credit := range credits {
		change, err := writeProgramEventCredit(ctx, store, eventID, credit, byID, seen)
		if err != nil {
			return result, err
		}
		if change != nil {
			result.changes = append(result.changes, *change)
		}
	}
	for _, current := range existing {
		if _, keep := seen[current.ID]; keep {
			continue
		}
		if err := store.RemoveCredit(ctx, current.ID); err != nil {
			return result, err
		}
		result.changes = append(result.changes, programEventCreditChange{id: current.ID, operation: sharedtelemetry.AuditItemOperationDeleted})
	}
	rows, err := store.Credits(ctx, eventID)
	if err != nil {
		return result, err
	}
	result.orderedIDs = creditIDs(rows)
	result.orderChanged = !sameStringSlice(previousIDs, result.orderedIDs)
	return result, nil
}

// writeProgramEventCredit creates credit, or updates the existing credit its
// ID names and marks it seen. It returns the change, or nil when an existing
// credit is unchanged.
func writeProgramEventCredit(
	ctx context.Context,
	store Store,
	eventID string,
	credit Credit,
	byID map[string]CreditRecord,
	seen map[string]struct{},
) (*programEventCreditChange, error) {
	if credit.ID == "" {
		row := CreditRecord{EventID: eventID, ArtistID: normalizedStringPtr(credit.ArtistID), MemberID: normalizedStringPtr(credit.MemberID), DisplayName: normalizedStringPtr(credit.DisplayName), CreditRole: normalizedStringPtr(credit.CreditRole), Description: normalizedStringPtr(credit.Description), SortOrder: credit.SortOrder, CreatedAt: time.Now().UTC()}
		if err := store.InsertCredit(ctx, &row); err != nil {
			return nil, err
		}
		return &programEventCreditChange{id: row.ID, operation: sharedtelemetry.AuditItemOperationCreated}, nil
	}
	current, ok := byID[credit.ID]
	if !ok {
		return nil, domainerr.InvalidField("credits", "credit id does not belong to program event")
	}
	if _, duplicate := seen[current.ID]; duplicate {
		return nil, domainerr.InvalidField("credits", "duplicate credit id")
	}
	seen[current.ID] = struct{}{}
	sortOrder := credit.SortOrder
	update := CreditUpdate{
		ArtistID:    setNullable(normalizedStringPtr(credit.ArtistID)),
		MemberID:    setNullable(normalizedStringPtr(credit.MemberID)),
		DisplayName: setNullable(normalizedStringPtr(credit.DisplayName)),
		CreditRole:  setNullable(normalizedStringPtr(credit.CreditRole)),
		Description: setNullable(normalizedStringPtr(credit.Description)),
		SortOrder:   &sortOrder,
	}
	if !creditChanged(update, current) {
		return nil, nil
	}
	if err := store.UpdateCredit(ctx, current.ID, update); err != nil {
		return nil, err
	}
	return &programEventCreditChange{id: current.ID, operation: sharedtelemetry.AuditItemOperationUpdated}, nil
}

func sameProgramEventCredits(left []CreditRecord, right []Credit) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if right[index].ID == "" || right[index].ID != left[index].ID || !sameNullableString(left[index].ArtistID, normalizedStringPtr(right[index].ArtistID)) || !sameNullableString(left[index].MemberID, normalizedStringPtr(right[index].MemberID)) || !sameNullableString(left[index].DisplayName, normalizedStringPtr(right[index].DisplayName)) || !sameNullableString(left[index].CreditRole, normalizedStringPtr(right[index].CreditRole)) || !sameNullableString(left[index].Description, normalizedStringPtr(right[index].Description)) || left[index].SortOrder != right[index].SortOrder {
			return false
		}
	}
	return true
}

// creditReader runs one lookup of a credit projection: in the caller's
// transaction, or in a snapshot of its own after a commit.
type creditReader func(read func(Store) error) error

func (s *Service) loadProgramEventCredits(ctx context.Context, store Store, eventID string) ([]CreditView, error) {
	rows, err := store.CreditsByID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	memberIDs := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].MemberID != nil {
			memberIDs = append(memberIDs, *rows[i].MemberID)
		}
	}
	summaries, err := s.creditMembers.LoadCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	inTransaction := func(read func(Store) error) error { return read(store) }
	result := make([]CreditView, 0, len(rows))
	for i := range rows {
		result = append(result, creditView(ctx, inTransaction, rows[i], summaries))
	}
	return result, nil
}

// committedCreditView projects a credit a mutation just committed; each
// lookup reads in its own snapshot.
func (s *Service) committedCreditView(ctx context.Context, row CreditRecord) CreditView {
	memberIDs := []string(nil)
	if row.MemberID != nil {
		memberIDs = append(memberIDs, *row.MemberID)
	}
	summaries, err := s.creditMembers.LoadCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		slog.ErrorContext(ctx, "Program Event credit member lookup failed; serving credit without member summary", "domain", "programevent", "operation", "load_credit_member_summaries", failurecause.Attr(err), "event_id", row.EventID, "credit_id", row.ID)
	}
	return creditView(ctx, func(read func(Store) error) error { return s.units.Read(ctx, read) }, row, summaries)
}

func creditView(ctx context.Context, read creditReader, row CreditRecord, summaries map[string]*commonv1.MemberSummary) CreditView {
	credit := CreditView{CreditRecord: row}
	if row.ArtistID != nil {
		credit.Artist = loadCreditArtist(ctx, read, row)
	}
	if row.MemberID != nil {
		credit.Member = summaries[*row.MemberID]
	}
	return credit
}

// loadCreditArtist is the credited artist of row with its first image, or
// nil when the artist cannot be read.
func loadCreditArtist(ctx context.Context, read creditReader, row CreditRecord) *CreditArtist {
	var artist CreditArtist
	err := read(func(store Store) error {
		var err error
		artist, err = store.CreditArtist(ctx, *row.ArtistID)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "Program Event credit artist lookup failed; serving credit without artist", "domain", "programevent", "operation", "load_credit_artist", failurecause.Attr(err), "event_id", row.EventID, "credit_id", row.ID)
		return nil
	}
	if artist.ID == "" {
		return nil
	}
	if imageAsset := loadCreditArtistImage(ctx, read, artist.ID); imageAsset != nil {
		artist.ImageAsset = imageAsset
	}
	return &artist
}

func loadCreditArtistImage(ctx context.Context, read creditReader, artistID string) *commonv1.AssetRef {
	var fileID string
	err := read(func(store Store) error {
		var err error
		fileID, err = store.ArtistImageFileID(ctx, artistID)
		return err
	})
	if err != nil || fileID == "" {
		if err != nil {
			slog.ErrorContext(ctx, "Program Event artist image lookup failed; serving artist without image", "domain", "programevent", "operation", "load_artist_image", failurecause.Attr(err), "artist_id", artistID)
		}
		return nil
	}
	var asset *commonv1.AssetRef
	err = read(func(store Store) error {
		var err error
		asset, err = store.ReadyImageAsset(ctx, fileID)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "Program Event artist image asset resolution failed; serving artist without image", "domain", "programevent", "operation", "resolve_artist_image_asset", failurecause.Attr(err), "artist_id", artistID, "file_id", fileID)
		return nil
	}
	return asset
}

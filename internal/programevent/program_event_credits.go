package programevent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
func (s *Service) AddProgramEventCredit(ctx context.Context, command AddProgramEventCreditCommand) (*managev1.ProgramEventCredit, error) {
	if strings.TrimSpace(command.EventID) == "" {
		return nil, domainerr.Required("event_id")
	}
	if countPresent(command.ArtistID, command.MemberID, command.DisplayName) != 1 {
		return nil, domainerr.InvalidField("credit", "exactly one of artist_id, user_id, display_name is required")
	}
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	credit, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.ProgramEventCredit]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (model.ProgramEventCredit, error) {
			return addProgramEventCredit(ctx, tx, command)
		},
		Audit: func(credit model.ProgramEventCredit) mutation.Audit {
			return childAudit(command.EventID, "credits", credit.ID, sharedtelemetry.AuditItemOperationCreated)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.toProtoProgramEventCredit(ctx, &credit), nil
}

func addProgramEventCredit(ctx context.Context, tx *gorm.DB, command AddProgramEventCreditCommand) (model.ProgramEventCredit, error) {
	if memberID := strings.TrimSpace(derefString(command.MemberID)); memberID != "" {
		if err := authorizationtarget.LockReferences(ctx, tx, []authorizationtarget.Reference{{
			MemberID: memberID,
			Field:    "member_id",
		}}); err != nil {
			return model.ProgramEventCredit{}, err
		}
	}
	var sortOrder int32
	if command.SortOrder != nil {
		sortOrder = *command.SortOrder
	} else {
		next, err := nextProgramEventCreditSortOrder(ctx, tx, command.EventID)
		if err != nil {
			return model.ProgramEventCredit{}, err
		}
		sortOrder = next
	}
	credit := model.ProgramEventCredit{
		EventID:     command.EventID,
		ArtistID:    normalizedStringPtr(command.ArtistID),
		MemberID:    normalizedStringPtr(command.MemberID),
		DisplayName: normalizedStringPtr(command.DisplayName),
		CreditRole:  normalizedStringPtr(command.CreditRole),
		Description: normalizedStringPtr(command.Description),
		SortOrder:   sortOrder,
		CreatedAt:   dbtime.Now(),
	}
	if err := tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&credit).Error; err != nil {
		return model.ProgramEventCredit{}, err
	}
	return credit, touchProgramEvent(ctx, tx, command.EventID)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
	credit  model.ProgramEventCredit
	changed bool
}

// UpdateProgramEventCredit changes one credit of the event.
func (s *Service) UpdateProgramEventCredit(ctx context.Context, command UpdateProgramEventCreditCommand) (*managev1.ProgramEventCredit, error) {
	if strings.TrimSpace(command.EventID) == "" {
		return nil, domainerr.Required("event_id")
	}
	if strings.TrimSpace(command.CreditID) == "" {
		return nil, domainerr.Required("credit_id")
	}
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[creditChange]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (creditChange, error) {
			return updateProgramEventCredit(ctx, tx, command)
		},
		Audit: func(change creditChange) mutation.Audit {
			if !change.changed {
				return mutation.Unchanged()
			}
			return childAudit(command.EventID, "credits", change.credit.ID, sharedtelemetry.AuditItemOperationUpdated)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.toProtoProgramEventCredit(ctx, &change.credit), nil
}

func updateProgramEventCredit(ctx context.Context, tx *gorm.DB, command UpdateProgramEventCreditCommand) (creditChange, error) {
	var credit model.ProgramEventCredit
	if err := loadProgramEventCredit(ctx, tx, command.EventID, command.CreditID, &credit); err != nil {
		return creditChange{}, err
	}
	update := programEventCreditUpdate{sortOrder: command.SortOrder}
	if command.CreditRole != nil {
		update.creditRole = setNullableColumn(normalizedStringPtr(command.CreditRole))
	}
	if command.Description != nil {
		update.description = setNullableColumn(normalizedStringPtr(command.Description))
	}
	if !update.changed(credit) {
		return creditChange{credit: credit}, nil
	}
	if err := tx.WithContext(ctx).Model(&credit).Updates(update.columns()).Error; err != nil {
		return creditChange{}, err
	}
	if err := tx.WithContext(ctx).
		First(&credit, "event_id = ? AND id = ?", command.EventID, command.CreditID).Error; err != nil {
		return creditChange{}, err
	}
	return creditChange{credit: credit, changed: true}, touchProgramEvent(ctx, tx, command.EventID)
}

func loadProgramEventCredit(ctx context.Context, tx *gorm.DB, eventID, creditID string, credit *model.ProgramEventCredit) error {
	if err := tx.WithContext(ctx).First(credit, "event_id = ? AND id = ?", eventID, creditID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("program event credit", creditID)
		}
		return err
	}
	return nil
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
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			var credit model.ProgramEventCredit
			if err := loadProgramEventCredit(ctx, tx, command.EventID, command.CreditID, &credit); err != nil {
				return "", err
			}
			result := tx.WithContext(ctx).
				Delete(&model.ProgramEventCredit{}, "event_id = ? AND id = ?", command.EventID, command.CreditID)
			if result.Error != nil {
				return "", result.Error
			}
			if result.RowsAffected == 0 {
				return "", domainerr.ResourceNotFound("program event credit", command.CreditID)
			}
			return credit.ID, touchProgramEvent(ctx, tx, command.EventID)
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
	return mutation.Run(ctx, s.mutations, mutation.Spec[CreditOrder]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (CreditOrder, error) {
			return reorderProgramEventCredits(ctx, tx, command)
		},
		Audit: func(order CreditOrder) mutation.Audit {
			if !order.Changed {
				return mutation.Unchanged()
			}
			return childOrderAudit(command.EventID, "credits", command.CreditIDs)
		},
	})
}

func reorderProgramEventCredits(ctx context.Context, tx *gorm.DB, command ReorderProgramEventCreditsCommand) (CreditOrder, error) {
	if err := validateProgramEventCreditOrder(ctx, tx, command); err != nil {
		return CreditOrder{}, err
	}
	var current []model.ProgramEventCredit
	if err := tx.WithContext(ctx).Where("event_id = ?", command.EventID).Order("sort_order ASC, created_at ASC").Find(&current).Error; err != nil {
		return CreditOrder{}, err
	}
	if len(current) != len(command.CreditIDs) {
		return CreditOrder{}, domainerr.InvalidField("credit_ids", "credit order must include every item")
	}
	currentIDs := make([]string, len(current))
	for index := range current {
		currentIDs[index] = current[index].ID
	}
	changed := !sameStringSlice(currentIDs, command.CreditIDs)
	if changed {
		for index, creditID := range command.CreditIDs {
			if err := tx.WithContext(ctx).
				Model(&model.ProgramEventCredit{}).
				Where("event_id = ? AND id = ?", command.EventID, creditID).
				Update("sort_order", index).Error; err != nil {
				return CreditOrder{}, err
			}
		}
	}
	updatedAt, err := programEventRelationMutationUpdatedAt(ctx, tx, command.EventID, changed)
	return CreditOrder{Changed: changed, UpdatedAt: updatedAt}, err
}

// validateProgramEventCreditOrder requires distinct credit IDs that all
// belong to the event.
func validateProgramEventCreditOrder(ctx context.Context, tx *gorm.DB, command ReorderProgramEventCreditsCommand) error {
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
	var count int64
	if err := tx.WithContext(ctx).
		Model(&model.ProgramEventCredit{}).
		Where("event_id = ? AND id IN ?", command.EventID, command.CreditIDs).
		Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(command.CreditIDs)) {
		return domainerr.InvalidField("credit_ids", "all credits must belong to the event")
	}
	return nil
}

// programEventCreditUpdate is an update to one credit row; unset and nil
// fields are left alone.
type programEventCreditUpdate struct {
	artistID    nullableColumn[string]
	memberID    nullableColumn[string]
	displayName nullableColumn[string]
	creditRole  nullableColumn[string]
	description nullableColumn[string]
	sortOrder   *int32
}

func (u programEventCreditUpdate) changed(credit model.ProgramEventCredit) bool {
	for _, column := range []struct {
		current *string
		update  nullableColumn[string]
	}{
		{credit.ArtistID, u.artistID},
		{credit.MemberID, u.memberID},
		{credit.DisplayName, u.displayName},
		{credit.CreditRole, u.creditRole},
		{credit.Description, u.description},
	} {
		if column.update.set && !sameNullableString(column.current, column.update.value) {
			return true
		}
	}
	return u.sortOrder != nil && credit.SortOrder != *u.sortOrder
}

func (u programEventCreditUpdate) columns() structured.Fields {
	columns := structured.Fields{}
	for column, update := range map[string]nullableColumn[string]{
		"artist_id":    u.artistID,
		"member_id":    u.memberID,
		"display_name": u.displayName,
		"credit_role":  u.creditRole,
		"description":  u.description,
	} {
		if update.set {
			columns[column] = update.columnValue()
		}
	}
	if u.sortOrder != nil {
		columns["sort_order"] = *u.sortOrder
	}
	return columns
}

func replaceProgramEventArtists(ctx context.Context, db *gorm.DB, eventID string, artists []Relation) error {
	return replaceProgramEventRelations(ctx, db, eventID, artists, func(
		artist Relation,
		now time.Time,
	) (model.ProgramEventArtist, error) {
		if artist.ID == "" {
			return model.ProgramEventArtist{}, domainerr.Required("artist_id")
		}
		return model.ProgramEventArtist{
			EventID: eventID, ArtistID: artist.ID, Role: artist.Role,
			SortOrder: artist.SortOrder, CreatedAt: now,
		}, nil
	})
}

func replaceProgramEventLabels(ctx context.Context, db *gorm.DB, eventID string, labels []Relation) error {
	return replaceProgramEventRelations(ctx, db, eventID, labels, func(
		label Relation,
		now time.Time,
	) (model.ProgramEventLabel, error) {
		if label.ID == "" {
			return model.ProgramEventLabel{}, domainerr.Required("label_id")
		}
		return model.ProgramEventLabel{
			EventID: eventID, LabelID: label.ID, Role: label.Role,
			SortOrder: label.SortOrder, CreatedAt: now,
		}, nil
	})
}

func replaceProgramEventClients(ctx context.Context, db *gorm.DB, eventID string, clients []Relation) error {
	return replaceProgramEventRelations(ctx, db, eventID, clients, func(
		client Relation,
		now time.Time,
	) (model.ProgramEventClient, error) {
		if client.ID == "" {
			return model.ProgramEventClient{}, domainerr.Required("client_id")
		}
		return model.ProgramEventClient{
			EventID: eventID, ClientID: client.ID, Role: client.Role,
			SortOrder: client.SortOrder, CreatedAt: now,
		}, nil
	})
}

func replaceProgramEventRelations[Row interface{}](
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	inputs []Relation,
	build func(Relation, time.Time) (Row, error),
) error {
	if err := db.WithContext(ctx).Delete(new(Row), "event_id = ?", eventID).Error; err != nil {
		return err
	}
	rows := make([]Row, 0, len(inputs))
	now := time.Now().UTC()
	for _, input := range inputs {
		row, err := build(input, now)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Create(&rows).Error; err != nil {
		return err
	}
	return nil
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

func replaceProgramEventCredits(ctx context.Context, db *gorm.DB, eventID string, credits []Credit) (programEventCreditReplacement, error) {
	result := programEventCreditReplacement{}
	references := make([]authorizationtarget.Reference, 0, len(credits))
	for i, credit := range credits {
		if countPresent(credit.ArtistID, credit.MemberID, credit.DisplayName) != 1 {
			return result, domainerr.InvalidField("credit", "exactly one of artist_id, user_id, display_name is required")
		}
		if credit.MemberID != nil {
			references = append(references, authorizationtarget.Reference{
				MemberID: *credit.MemberID,
				Field:    fmt.Sprintf("credit[%d].member_id", i),
			})
		}
	}
	if err := authorizationtarget.LockReferences(ctx, db, references); err != nil {
		return result, err
	}
	var existing []model.ProgramEventCredit
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("event_id = ?", eventID).Order("sort_order ASC, created_at ASC").Find(&existing).Error; err != nil {
		return result, err
	}
	previousIDs := make([]string, len(existing))
	byID := make(map[string]model.ProgramEventCredit, len(existing))
	for index := range existing {
		previousIDs[index] = existing[index].ID
		byID[existing[index].ID] = existing[index]
	}
	seen := make(map[string]struct{}, len(credits))
	for _, credit := range credits {
		change, err := writeProgramEventCredit(ctx, db, eventID, credit, byID, seen)
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
		if err := db.WithContext(ctx).Delete(&current).Error; err != nil {
			return result, err
		}
		result.changes = append(result.changes, programEventCreditChange{id: current.ID, operation: sharedtelemetry.AuditItemOperationDeleted})
	}
	rows, err := loadProgramEventCreditRows(ctx, db, eventID)
	if err != nil {
		return result, err
	}
	result.orderedIDs = make([]string, len(rows))
	for index := range rows {
		result.orderedIDs[index] = rows[index].ID
	}
	result.orderChanged = !sameStringSlice(previousIDs, result.orderedIDs)
	return result, nil
}

// writeProgramEventCredit creates credit, or updates the existing credit its
// ID names and marks it seen. It returns the change, or nil when an existing
// credit is unchanged.
func writeProgramEventCredit(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	credit Credit,
	byID map[string]model.ProgramEventCredit,
	seen map[string]struct{},
) (*programEventCreditChange, error) {
	if credit.ID == "" {
		row := model.ProgramEventCredit{EventID: eventID, ArtistID: normalizedStringPtr(credit.ArtistID), MemberID: normalizedStringPtr(credit.MemberID), DisplayName: normalizedStringPtr(credit.DisplayName), CreditRole: normalizedStringPtr(credit.CreditRole), Description: normalizedStringPtr(credit.Description), SortOrder: credit.SortOrder, CreatedAt: time.Now().UTC()}
		if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
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
	update := programEventCreditUpdate{
		artistID:    setNullableColumn(normalizedStringPtr(credit.ArtistID)),
		memberID:    setNullableColumn(normalizedStringPtr(credit.MemberID)),
		displayName: setNullableColumn(normalizedStringPtr(credit.DisplayName)),
		creditRole:  setNullableColumn(normalizedStringPtr(credit.CreditRole)),
		description: setNullableColumn(normalizedStringPtr(credit.Description)),
		sortOrder:   &sortOrder,
	}
	if !update.changed(current) {
		return nil, nil
	}
	if err := db.WithContext(ctx).Model(&current).Updates(update.columns()).Error; err != nil {
		return nil, err
	}
	return &programEventCreditChange{id: current.ID, operation: sharedtelemetry.AuditItemOperationUpdated}, nil
}

func loadProgramEventCreditRows(ctx context.Context, db *gorm.DB, eventID string) ([]model.ProgramEventCredit, error) {
	var rows []model.ProgramEventCredit
	if err := db.WithContext(ctx).Where("event_id = ?", eventID).Order("sort_order ASC, created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func sameProgramEventCredits(left []model.ProgramEventCredit, right []Credit) bool {
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

func loadProgramEventArtists(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventArtist, error) {
	return loadProgramEventRelations(
		ctx, db, eventID, "sort_order ASC, artist_id ASC",
		func(row model.ProgramEventArtist) *managev1.ProgramEventArtist {
			return &managev1.ProgramEventArtist{ArtistId: row.ArtistID, Role: row.Role, SortOrder: row.SortOrder}
		},
	)
}

func loadProgramEventLabels(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventLabel, error) {
	return loadProgramEventRelations(
		ctx, db, eventID, "sort_order ASC, label_id ASC",
		func(row model.ProgramEventLabel) *managev1.ProgramEventLabel {
			return &managev1.ProgramEventLabel{LabelId: row.LabelID, Role: row.Role, SortOrder: row.SortOrder}
		},
	)
}

func loadProgramEventClients(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventClient, error) {
	return loadProgramEventRelations(
		ctx, db, eventID, "sort_order ASC, client_id ASC",
		func(row model.ProgramEventClient) *managev1.ProgramEventClient {
			return &managev1.ProgramEventClient{ClientId: row.ClientID, Role: row.Role, SortOrder: row.SortOrder}
		},
	)
}

func loadProgramEventRelations[Row interface{}, Result interface{}](
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	order string,
	convert func(Row) *Result,
) ([]*Result, error) {
	var rows []Row
	if err := db.WithContext(ctx).Where("event_id = ?", eventID).Order(order).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*Result, 0, len(rows))
	for _, row := range rows {
		result = append(result, convert(row))
	}
	return result, nil
}

func (s *Service) loadProgramEventCredits(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventCredit, error) {
	var rows []model.ProgramEventCredit
	if err := db.WithContext(ctx).Where("event_id = ?", eventID).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
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
	result := make([]*managev1.ProgramEventCredit, 0, len(rows))
	for i := range rows {
		result = append(result, toProtoProgramEventCredit(ctx, db, s.media, &rows[i], summaries))
	}
	return result, nil
}

func (s *Service) toProtoProgramEventCredit(ctx context.Context, row *model.ProgramEventCredit) *managev1.ProgramEventCredit {
	memberIDs := []string(nil)
	if row != nil && row.MemberID != nil {
		memberIDs = append(memberIDs, *row.MemberID)
	}
	summaries, err := s.creditMembers.LoadCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		slog.ErrorContext(ctx, "Program Event credit member lookup failed; serving credit without member summary", "domain", "programevent", "operation", "load_credit_member_summaries", failurecause.Attr(err), "event_id", row.EventID, "credit_id", row.ID)
	}
	return toProtoProgramEventCredit(ctx, s.db, s.media, row, summaries)
}

func toProtoProgramEventCredit(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, row *model.ProgramEventCredit, summaries map[string]*commonv1.MemberSummary) *managev1.ProgramEventCredit {
	credit := &managev1.ProgramEventCredit{
		Id:          row.ID,
		ArtistId:    row.ArtistID,
		MemberId:    row.MemberID,
		DisplayName: row.DisplayName,
		CreditRole:  row.CreditRole,
		Description: row.Description,
		SortOrder:   row.SortOrder,
	}

	if row.ArtistID != nil {
		var artist struct {
			ID   string  `gorm:"column:id"`
			Name string  `gorm:"column:name"`
			Slug *string `gorm:"column:slug"`
		}
		if err := db.WithContext(ctx).
			Table("artist").
			Select("artist.id, "+artistSourceTitleSQL("artist")+" AS name, artist.slug").
			Where("artist.id = ?", *row.ArtistID).
			Scan(&artist).Error; err == nil && artist.ID != "" {
			credit.Artist = &managev1.ProgramEventCreditArtist{
				Id:   artist.ID,
				Name: artist.Name,
				Slug: artist.Slug,
			}
			if imageAsset := loadProgramEventArtistImageAsset(ctx, db, mediaAssets, artist.ID); imageAsset != nil {
				credit.Artist.ImageAsset = imageAsset
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Program Event credit artist lookup failed; serving credit without artist", "domain", "programevent", "operation", "load_credit_artist", failurecause.Attr(err), "event_id", row.EventID, "credit_id", row.ID)
		}
	}

	if row.MemberID != nil {
		credit.Member = summaries[*row.MemberID]
	}

	return credit
}

func artistSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return "COALESCE((SELECT translation.title FROM artist_translation AS translation WHERE translation.entity_id = " + alias + ".id AND translation.locale = " + alias + ".source_locale LIMIT 1), '')"
}

func loadProgramEventArtistImageAsset(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, artistID string) *commonv1.AssetRef {
	var result struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).
		Table("artist_file").
		Select("file_id").
		Where("artist_file.artist_id = ?", artistID).
		Order("artist_file.sort_order ASC").
		Limit(1).
		Scan(&result).Error; err != nil || result.FileID == "" {
		if err != nil {
			slog.ErrorContext(ctx, "Program Event artist image lookup failed; serving artist without image", "domain", "programevent", "operation", "load_artist_image", failurecause.Attr(err), "artist_id", artistID)
		}
		return nil
	}
	asset, err := mediaAssets.ReadyPublicAssetRefForSourceFile(ctx, db, result.FileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "Program Event artist image asset resolution failed; serving artist without image", "domain", "programevent", "operation", "resolve_artist_image_asset", failurecause.Attr(err), "artist_id", artistID, "file_id", result.FileID)
		return nil
	}
	return asset
}

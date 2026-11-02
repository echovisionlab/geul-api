package programeventpostgres

import (
	"context"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func creditFromRow(row model.ProgramEventCredit) programevent.CreditRecord {
	return programevent.CreditRecord{
		ID: row.ID, EventID: row.EventID, ArtistID: row.ArtistID, MemberID: row.MemberID,
		DisplayName: row.DisplayName, CreditRole: row.CreditRole, Description: row.Description,
		SortOrder: row.SortOrder, CreatedAt: row.CreatedAt,
	}
}

func creditRows(rows []model.ProgramEventCredit) []programevent.CreditRecord {
	credits := make([]programevent.CreditRecord, 0, len(rows))
	for _, row := range rows {
		credits = append(credits, creditFromRow(row))
	}
	return credits
}

func (s *store) NextCreditSortOrder(ctx context.Context, eventID string) (int32, error) {
	var row struct {
		SortOrder int32 `gorm:"column:sort_order"`
	}
	if err := s.tx.WithContext(ctx).
		Raw("SELECT COALESCE(MAX(sort_order), -1) + 1 AS sort_order FROM program_event_credit WHERE event_id = ?", eventID).
		Scan(&row).Error; err != nil {
		return 0, err
	}
	return row.SortOrder, nil
}

func (s *store) InsertCredit(ctx context.Context, credit *programevent.CreditRecord) error {
	row := model.ProgramEventCredit{
		EventID: credit.EventID, ArtistID: credit.ArtistID, MemberID: credit.MemberID,
		DisplayName: credit.DisplayName, CreditRole: credit.CreditRole, Description: credit.Description,
		SortOrder: credit.SortOrder, CreatedAt: credit.CreatedAt,
	}
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return err
	}
	*credit = creditFromRow(row)
	return nil
}

func (s *store) Credit(ctx context.Context, eventID, creditID string) (programevent.CreditRecord, error) {
	var row model.ProgramEventCredit
	if err := s.tx.WithContext(ctx).First(&row, "event_id = ? AND id = ?", eventID, creditID).Error; err != nil {
		return programevent.CreditRecord{}, notFound(err, programevent.ErrCreditNotFound)
	}
	return creditFromRow(row), nil
}

// creditColumns is the program_event_credit row update for the fields
// update sets.
func creditColumns(update programevent.CreditUpdate) structured.Fields {
	columns := structured.Fields{}
	for column, value := range map[string]programevent.Nullable[string]{
		"artist_id":    update.ArtistID,
		"member_id":    update.MemberID,
		"display_name": update.DisplayName,
		"credit_role":  update.CreditRole,
		"description":  update.Description,
	} {
		if value.Set {
			columns[column] = nullableValue(value)
		}
	}
	if update.SortOrder != nil {
		columns["sort_order"] = *update.SortOrder
	}
	return columns
}

func (s *store) UpdateCredit(ctx context.Context, creditID string, update programevent.CreditUpdate) error {
	return s.tx.WithContext(ctx).Model(&model.ProgramEventCredit{ID: creditID}).Updates(creditColumns(update)).Error
}

func (s *store) DeleteCredit(ctx context.Context, eventID, creditID string) (bool, error) {
	result := s.tx.WithContext(ctx).
		Delete(&model.ProgramEventCredit{}, "event_id = ? AND id = ?", eventID, creditID)
	return result.RowsAffected > 0, result.Error
}

func (s *store) RemoveCredit(ctx context.Context, creditID string) error {
	return s.tx.WithContext(ctx).Delete(&model.ProgramEventCredit{ID: creditID}).Error
}

func (s *store) Credits(ctx context.Context, eventID string) ([]programevent.CreditRecord, error) {
	var rows []model.ProgramEventCredit
	if err := s.tx.WithContext(ctx).Where("event_id = ?", eventID).Order("sort_order ASC, created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return creditRows(rows), nil
}

func (s *store) LockCredits(ctx context.Context, eventID string) ([]programevent.CreditRecord, error) {
	var rows []model.ProgramEventCredit
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("event_id = ?", eventID).Order("sort_order ASC, created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return creditRows(rows), nil
}

func (s *store) CreditsByID(ctx context.Context, eventID string) ([]programevent.CreditRecord, error) {
	var rows []model.ProgramEventCredit
	if err := s.tx.WithContext(ctx).Where("event_id = ?", eventID).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return creditRows(rows), nil
}

func (s *store) CountCredits(ctx context.Context, eventID string, creditIDs []string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).
		Model(&model.ProgramEventCredit{}).
		Where("event_id = ? AND id IN ?", eventID, creditIDs).
		Count(&count).Error
	return count, err
}

func (s *store) PlaceCredit(ctx context.Context, eventID, creditID string, position int) error {
	return s.tx.WithContext(ctx).
		Model(&model.ProgramEventCredit{}).
		Where("event_id = ? AND id = ?", eventID, creditID).
		Update("sort_order", position).Error
}

func (s *store) LockCreditMembers(ctx context.Context, references []programevent.CreditMemberReference) error {
	targets := make([]authorizationtarget.Reference, 0, len(references))
	for _, reference := range references {
		targets = append(targets, authorizationtarget.Reference{MemberID: reference.MemberID, Field: reference.Field})
	}
	return authorizationtarget.LockReferences(ctx, s.tx, targets)
}

func (s *store) CreditArtist(ctx context.Context, artistID string) (programevent.CreditArtist, error) {
	id, name, slug, err := s.ports.CreditArtists.CreditArtist(ctx, s.tx, artistID)
	return programevent.CreditArtist{ID: id, Name: name, Slug: slug}, err
}

func (s *store) ArtistImageFileID(ctx context.Context, artistID string) (string, error) {
	return s.ports.CreditArtists.FirstImageFileID(ctx, s.tx, artistID)
}

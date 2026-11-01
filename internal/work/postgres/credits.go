package workpostgres

import (
	"context"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/work"
)

var creditedWorkSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":      WorkSourceTitleSQL("w"),
		"type":       "w.type",
		"status":     "w.status",
		"created_at": "w.created_at",
		"updated_at": "w.updated_at",
	},
	DefaultSort: "w.created_at DESC, wc.id DESC",
}

var creditedWorkFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{WorkSourceTitleSQL("w")},
		},
		"title": {
			Column:     WorkSourceTitleSQL("w"),
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.TextOps,
		},
		"type": {
			Column:     "w.type",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: WorkTypeValues(),
		},
		"status": {
			Column:     "w.status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: work.Statuses(),
		},
	},
}

func creditGroupFromRow(row model.WorkCreditGroup) work.CreditGroup {
	return work.CreditGroup{ID: row.ID, WorkID: row.WorkID, Name: row.Name, SortOrder: row.SortOrder}
}

func creditFromRow(row model.WorkCredit) work.Credit {
	return work.Credit{
		ID: row.ID, WorkID: row.WorkID, GroupID: row.GroupID, ArtistID: row.ArtistID, MemberID: row.MemberID,
		Name: row.Name, CreditRole: row.CreditRole, SortOrder: row.SortOrder,
	}
}

func (s *store) CreditRows(ctx context.Context, workID string) ([]work.CreditGroup, []work.Credit, error) {
	var groupRows []model.WorkCreditGroup
	if err := s.tx.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Order("id ASC").
		Find(&groupRows).Error; err != nil {
		return nil, nil, err
	}

	var creditRows []model.WorkCredit
	if err := s.tx.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Order("id ASC").
		Find(&creditRows).Error; err != nil {
		return nil, nil, err
	}
	groups := make([]work.CreditGroup, len(groupRows))
	for i := range groupRows {
		groups[i] = creditGroupFromRow(groupRows[i])
	}
	credits := make([]work.Credit, len(creditRows))
	for i := range creditRows {
		credits[i] = creditFromRow(creditRows[i])
	}
	return groups, credits, nil
}

func (s *store) FindCreditGroup(ctx context.Context, groupID string) (work.CreditGroup, error) {
	var row model.WorkCreditGroup
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", groupID).Error; err != nil {
		return work.CreditGroup{}, notFound(err, work.ErrCreditGroupNotFound)
	}
	return creditGroupFromRow(row), nil
}

func (s *store) LockCreditGroup(ctx context.Context, groupID, workID string) (work.CreditGroup, error) {
	row := model.WorkCreditGroup{ID: groupID, WorkID: workID}
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ? AND work_id = ?", groupID, workID).Error; err != nil {
		return work.CreditGroup{}, notFound(err, work.ErrCreditGroupNotFound)
	}
	return creditGroupFromRow(row), nil
}

func (s *store) CreditGroupInWork(ctx context.Context, workID, groupID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.WorkCreditGroup{}).
		Where("id = ? AND work_id = ?", groupID, workID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) CreditItemExists(ctx context.Context, itemID string) (bool, error) {
	var groupCount, creditCount int64
	if err := s.tx.WithContext(ctx).Model(&model.WorkCreditGroup{}).Where("id = ?", itemID).Count(&groupCount).Error; err != nil {
		return false, err
	}
	if err := s.tx.WithContext(ctx).Model(&model.WorkCredit{}).Where("id = ?", itemID).Count(&creditCount).Error; err != nil {
		return false, err
	}
	return groupCount > 0 || creditCount > 0, nil
}

func (s *store) InsertCreditGroup(ctx context.Context, group *work.CreditGroup) error {
	row := model.WorkCreditGroup{WorkID: group.WorkID, Name: group.Name, SortOrder: group.SortOrder}
	if err := s.tx.WithContext(ctx).Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&row).Error; err != nil {
		return err
	}
	group.ID = row.ID
	return nil
}

func (s *store) RenameCreditGroup(ctx context.Context, groupID, name string) error {
	return s.tx.WithContext(ctx).Model(&model.WorkCreditGroup{ID: groupID}).Updates(structured.Fields{"name": name}).Error
}

func (s *store) DeleteCreditGroup(ctx context.Context, groupID string) error {
	return s.tx.WithContext(ctx).Delete(&model.WorkCreditGroup{ID: groupID}).Error
}

func (s *store) FindCredit(ctx context.Context, creditID string) (work.Credit, error) {
	var row model.WorkCredit
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", creditID).Error; err != nil {
		return work.Credit{}, notFound(err, work.ErrCreditNotFound)
	}
	return creditFromRow(row), nil
}

func (s *store) LockCredit(ctx context.Context, creditID, workID string) (work.Credit, error) {
	row := model.WorkCredit{ID: creditID, WorkID: workID}
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ? AND work_id = ?", creditID, workID).Error; err != nil {
		return work.Credit{}, notFound(err, work.ErrCreditNotFound)
	}
	return creditFromRow(row), nil
}

func (s *store) InsertCredit(ctx context.Context, credit *work.Credit) error {
	row := model.WorkCredit{
		WorkID: credit.WorkID, GroupID: credit.GroupID, ArtistID: credit.ArtistID, MemberID: credit.MemberID,
		Name: credit.Name, CreditRole: credit.CreditRole, SortOrder: credit.SortOrder,
	}
	if err := s.tx.WithContext(ctx).Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&row).Error; err != nil {
		return err
	}
	credit.ID = row.ID
	return nil
}

func (s *store) UpdateCredit(ctx context.Context, creditID string, setGroup bool, groupID *string, role *string) error {
	updates := structured.Fields{}
	if setGroup {
		updates["group_id"] = groupID
	}
	if role != nil {
		updates["credit_role"] = *role
	}
	return s.tx.WithContext(ctx).Model(&model.WorkCredit{ID: creditID}).Updates(updates).Error
}

func (s *store) UngroupCredit(ctx context.Context, creditID, workID string) error {
	return s.tx.WithContext(ctx).Model(&model.WorkCredit{}).
		Where("id = ? AND work_id = ?", creditID, workID).
		Update("group_id", nil).Error
}

func (s *store) DeleteCredit(ctx context.Context, creditID string) error {
	return s.tx.WithContext(ctx).Delete(&model.WorkCredit{ID: creditID}).Error
}

func (s *store) SetGroupSortOrder(ctx context.Context, workID, groupID string, sortOrder int) error {
	result := s.tx.WithContext(ctx).
		Model(&model.WorkCreditGroup{}).
		Where("id = ? AND work_id = ?", groupID, workID).
		Update("sort_order", sortOrder)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return rowChanged
	}
	return nil
}

func (s *store) SetCreditSortOrder(ctx context.Context, workID, creditID string, groupID *string, sortOrder int) error {
	var group any
	if groupID != nil {
		group = *groupID
	}
	result := s.tx.WithContext(ctx).
		Model(&model.WorkCredit{}).
		Where("id = ? AND work_id = ?", creditID, workID).
		Updates(map[string]any{"group_id": group, "sort_order": sortOrder})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return rowChanged
	}
	return nil
}

func (s *store) LockCreditMember(ctx context.Context, memberID string) error {
	return authorizationtarget.LockReferences(ctx, s.tx, []authorizationtarget.Reference{{
		MemberID: memberID,
		Field:    "member_id",
	}})
}

func (s *store) CreditArtist(ctx context.Context, artistID string) (work.CreditArtistRow, bool, error) {
	var artist struct {
		ID          string
		Name        string
		Slug        *string
		ImageFileID *string `gorm:"column:image_file_id"`
	}
	err := s.tx.WithContext(ctx).
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
		return work.CreditArtistRow{}, false, err
	}
	return work.CreditArtistRow{ID: artist.ID, Name: artist.Name, Slug: artist.Slug, ImageFileID: artist.ImageFileID}, true, nil
}

func (s *store) CreditingArtistIDs(ctx context.Context, memberID string) ([]string, error) {
	var artistIDs []string
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT artist_id::text FROM artist_owner WHERE member_id = ?::uuid
		UNION
		SELECT artist_id::text FROM artist_manager WHERE member_id = ?::uuid
	`, memberID, memberID).Scan(&artistIDs).Error; err != nil {
		return nil, err
	}
	return artistIDs, nil
}

func (s *store) ListCreditedWorks(ctx context.Context, page work.CreditedPage) ([]work.CreditedRow, int64, error) {
	stored := s.tx.WithContext(ctx).
		Table("work_credit wc").
		Joins("JOIN work w ON w.id = wc.work_id").
		Joins("LEFT JOIN artist a ON a.id = wc.artist_id").
		Joins(`
			LEFT JOIN (
				SELECT DISTINCT ON (artist_id) artist_id, file_id
				FROM artist_file
				ORDER BY artist_id, sort_order ASC, created_at ASC
			) afi ON afi.artist_id = a.id
		`)

	if len(page.ArtistIDs) > 0 {
		stored = stored.Where("(wc.member_id = ? OR wc.artist_id IN ?)", page.MemberID, page.ArtistIDs)
	} else {
		stored = stored.Where("wc.member_id = ?", page.MemberID)
	}

	stored, err := creditedWorkFilterConfig.ApplyFilters(stored, page.Filters)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	stored, err = creditedWorkSortConfig.ApplySort(stored, page.Sorts)
	if err != nil {
		return nil, 0, err
	}

	var rows []work.CreditedRow
	err = queryutil.Pagination{Limit: page.Limit, Offset: page.Offset}.Apply(stored).
		Select(`
			w.id AS work_id,
 w.source_locale AS source_locale,
			` + WorkSourceTitleSQL("w") + ` AS title,
			w.slug AS slug,
			w.type AS type,
			w.status AS status,
			w.created_at AS created_at,
			wc.id AS credit_id,
			wc.credit_role AS credit_role,
			wc.artist_id AS artist_id,
			` + ArtistSourceTitleSQL("a") + ` AS artist_name,
			afi.file_id AS artist_image_file_id,
			wc.member_id AS member_id
		`).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

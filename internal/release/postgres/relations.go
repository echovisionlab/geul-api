package releasepostgres

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/release"
)

func (s *store) ReleaseArtists(ctx context.Context, releaseID string) ([]release.ReleaseArtist, error) {
	var rows []model.ReleaseArtist
	if err := s.tx.WithContext(ctx).Where("release_id = ?", releaseID).
		Order("sort_order ASC, artist_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	artists := make([]release.ReleaseArtist, len(rows))
	for i, row := range rows {
		artists[i] = release.ReleaseArtist{ArtistID: row.ArtistID, SortOrder: row.SortOrder}
	}
	return artists, nil
}

func (s *store) ReleaseArtistsByCreation(ctx context.Context, releaseID string) ([]release.ReleaseArtist, error) {
	var rows []struct {
		ArtistID  string `gorm:"column:artist_id"`
		SortOrder int    `gorm:"column:sort_order"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_artist").
		Select("release_artist.artist_id, release_artist.sort_order").
		Where("release_artist.release_id = ?", releaseID).
		Order("release_artist.sort_order ASC, release_artist.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	artists := make([]release.ReleaseArtist, len(rows))
	for i, row := range rows {
		artists[i] = release.ReleaseArtist{ArtistID: row.ArtistID, SortOrder: row.SortOrder}
	}
	return artists, nil
}

func (s *store) ReplaceReleaseArtists(ctx context.Context, releaseID string, current, next []release.ReleaseArtist) error {
	tx := s.tx.WithContext(ctx)
	nextByID := make(map[string]release.ReleaseArtist, len(next))
	for _, row := range next {
		nextByID[row.ArtistID] = row
	}
	currentByID := make(map[string]release.ReleaseArtist, len(current))
	for _, row := range current {
		currentByID[row.ArtistID] = row
		if _, exists := nextByID[row.ArtistID]; !exists {
			if err := tx.Delete(&model.ReleaseArtist{}, "release_id = ? AND artist_id = ?", releaseID, row.ArtistID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.ArtistID]
		if !exists {
			if err := tx.Create(&model.ReleaseArtist{ReleaseID: releaseID, ArtistID: row.ArtistID, SortOrder: row.SortOrder}).Error; err != nil {
				return err
			}
			continue
		}
		if previous.SortOrder != row.SortOrder {
			if err := tx.Model(&model.ReleaseArtist{}).
				Where("release_id = ? AND artist_id = ?", releaseID, row.ArtistID).
				Update("sort_order", row.SortOrder).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *store) ReleaseLabels(ctx context.Context, releaseID string) ([]release.ReleaseLabel, error) {
	var rows []model.ReleaseLabel
	if err := s.tx.WithContext(ctx).Where("release_id = ?", releaseID).
		Order("sort_order ASC, label_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	labels := make([]release.ReleaseLabel, len(rows))
	for i, row := range rows {
		labels[i] = release.ReleaseLabel{LabelID: row.LabelID, CatalogNumber: row.CatalogNumber, SortOrder: row.SortOrder}
	}
	return labels, nil
}

func (s *store) ReplaceReleaseLabels(ctx context.Context, releaseID string, current, next []release.ReleaseLabel) error {
	tx := s.tx.WithContext(ctx)
	nextByID := make(map[string]release.ReleaseLabel, len(next))
	for _, row := range next {
		nextByID[row.LabelID] = row
	}
	currentByID := make(map[string]release.ReleaseLabel, len(current))
	for _, row := range current {
		currentByID[row.LabelID] = row
		if _, exists := nextByID[row.LabelID]; !exists {
			if err := tx.Delete(&model.ReleaseLabel{}, "release_id = ? AND label_id = ?", releaseID, row.LabelID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.LabelID]
		if !exists {
			if err := tx.Create(&model.ReleaseLabel{
				ReleaseID: releaseID, LabelID: row.LabelID, CatalogNumber: row.CatalogNumber, SortOrder: row.SortOrder,
			}).Error; err != nil {
				return err
			}
			continue
		}
		updates := map[string]any{}
		if !sameOptionalString(previous.CatalogNumber, row.CatalogNumber) {
			updates["catalog_number"] = row.CatalogNumber
		}
		if previous.SortOrder != row.SortOrder {
			updates["sort_order"] = row.SortOrder
		}
		if len(updates) > 0 {
			if err := tx.Model(&model.ReleaseLabel{}).
				Where("release_id = ? AND label_id = ?", releaseID, row.LabelID).
				Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *store) ReleaseFormats(ctx context.Context, releaseID string) ([]release.ReleaseFormat, error) {
	var rows []model.ReleaseFormat
	if err := s.tx.WithContext(ctx).Where("release_id = ?", releaseID).Order("format_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	formats := make([]release.ReleaseFormat, len(rows))
	for i, row := range rows {
		formats[i] = release.ReleaseFormat{FormatID: row.FormatID, FormatDescription: row.FormatDescription}
	}
	return formats, nil
}

func (s *store) ReplaceReleaseFormats(ctx context.Context, releaseID string, current, next []release.ReleaseFormat) error {
	tx := s.tx.WithContext(ctx)
	nextByID := make(map[string]release.ReleaseFormat, len(next))
	for _, row := range next {
		nextByID[row.FormatID] = row
	}
	currentByID := make(map[string]release.ReleaseFormat, len(current))
	for _, row := range current {
		currentByID[row.FormatID] = row
		if _, exists := nextByID[row.FormatID]; !exists {
			if err := tx.Delete(&model.ReleaseFormat{}, "release_id = ? AND format_id = ?", releaseID, row.FormatID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.FormatID]
		if !exists {
			if err := tx.Create(&model.ReleaseFormat{
				ReleaseID: releaseID, FormatID: row.FormatID, FormatDescription: row.FormatDescription,
			}).Error; err != nil {
				return err
			}
			continue
		}
		if !sameOptionalString(previous.FormatDescription, row.FormatDescription) {
			if err := tx.Model(&model.ReleaseFormat{}).
				Where("release_id = ? AND format_id = ?", releaseID, row.FormatID).
				Update("format_description", row.FormatDescription).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *store) ReleaseCredits(ctx context.Context, releaseID string) ([]release.ReleaseCredit, error) {
	var rows []model.ReleaseCredit
	if err := s.tx.WithContext(ctx).Where("release_id = ?", releaseID).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]release.ReleaseCredit, len(rows))
	for i, row := range rows {
		credits[i] = release.ReleaseCredit{
			ID: row.ID, ArtistID: row.ArtistID, MemberID: row.MemberID, CreditedName: row.CreditedName,
			CreditRole: row.CreditRole, SortOrder: row.SortOrder,
		}
	}
	return credits, nil
}

func (s *store) ReplaceReleaseCredits(ctx context.Context, releaseID string, current, next []release.ReleaseCredit) error {
	tx := s.tx.WithContext(ctx)
	nextByID := make(map[string]release.ReleaseCredit, len(next))
	for _, row := range next {
		nextByID[row.ID] = row
	}
	currentByID := make(map[string]release.ReleaseCredit, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
		if _, exists := nextByID[row.ID]; !exists {
			if err := tx.Delete(&model.ReleaseCredit{}, "release_id = ? AND id = ?", releaseID, row.ID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.ID]
		if !exists {
			if err := tx.Create(&model.ReleaseCredit{
				ID: row.ID, ReleaseID: releaseID, ArtistID: row.ArtistID, MemberID: row.MemberID,
				CreditedName: row.CreditedName, CreditRole: row.CreditRole, SortOrder: row.SortOrder,
			}).Error; err != nil {
				return err
			}
			continue
		}
		updates := creditUpdateMap(
			creditValues{previous.ArtistID, previous.MemberID, previous.CreditedName, previous.CreditRole, previous.SortOrder},
			creditValues{row.ArtistID, row.MemberID, row.CreditedName, row.CreditRole, row.SortOrder},
		)
		if len(updates) > 0 {
			if err := tx.Model(&model.ReleaseCredit{}).
				Where("release_id = ? AND id = ?", releaseID, row.ID).
				Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// creditValues are the stored values a Release or Track credit row shares.
type creditValues struct {
	artistID, memberID, creditedName, creditRole *string
	sortOrder                                    int
}

// creditUpdateMap is the column update from previous to next.
func creditUpdateMap(previous, next creditValues) map[string]any {
	updates := make(map[string]any)
	if !sameOptionalString(previous.artistID, next.artistID) {
		updates["artist_id"] = next.artistID
	}
	if !sameOptionalString(previous.memberID, next.memberID) {
		updates["member_id"] = next.memberID
	}
	if !sameOptionalString(previous.creditedName, next.creditedName) {
		updates["credited_name"] = next.creditedName
	}
	if !sameOptionalString(previous.creditRole, next.creditRole) {
		updates["credit_role"] = next.creditRole
	}
	if previous.sortOrder != next.sortOrder {
		updates["sort_order"] = next.sortOrder
	}
	return updates
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func (s *store) CreditIDs(ctx context.Context, releaseID string) ([]string, error) {
	var creditIDs []string
	if err := s.tx.WithContext(ctx).
		Table("release_credit").
		Where("release_id = ?", releaseID).
		Order("id ASC").
		Pluck("id", &creditIDs).Error; err != nil {
		return nil, err
	}
	return creditIDs, nil
}

// referenceTable is the relation table, its reference column and the
// referenced table of a reference relation.
type referenceTable struct {
	table, column, reference string
	row                      func(releaseID, referenceID string) any
}

func referenceTableOf(relation release.ReferenceRelation) (referenceTable, error) {
	switch relation {
	case release.ReferenceCategories:
		return referenceTable{"release_category", "category_id", "category", func(releaseID, id string) any {
			return &model.ReleaseCategory{ReleaseID: releaseID, CategoryID: id}
		}}, nil
	case release.ReferenceGenres:
		return referenceTable{"release_genre", "genre_id", "genre", func(releaseID, id string) any {
			return &model.ReleaseGenre{ReleaseID: releaseID, GenreID: id}
		}}, nil
	case release.ReferenceStyles:
		return referenceTable{"release_style", "style_id", "style", func(releaseID, id string) any {
			return &model.ReleaseStyle{ReleaseID: releaseID, StyleID: id}
		}}, nil
	default:
		return referenceTable{}, fmt.Errorf("%w %d", errInvalidRelation, relation)
	}
}

func (s *store) ReferenceIDs(ctx context.Context, relation release.ReferenceRelation, releaseID string) ([]string, error) {
	table, err := referenceTableOf(relation)
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := s.tx.WithContext(ctx).Table(table.table).Where("release_id = ?", releaseID).Pluck(table.column, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *store) ReplaceReferences(ctx context.Context, relation release.ReferenceRelation, releaseID string, removed, added []string) error {
	table, err := referenceTableOf(relation)
	if err != nil {
		return err
	}
	tx := s.tx.WithContext(ctx)
	if len(removed) > 0 {
		query := "DELETE FROM " + table.table + " WHERE release_id = ? AND " + table.column + " IN ?"
		if err := tx.Exec(query, releaseID, removed).Error; err != nil {
			return err
		}
	}
	for _, id := range added {
		if err := tx.Create(table.row(releaseID, id)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *store) References(ctx context.Context, relation release.ReferenceRelation, releaseID string) ([]release.Reference, error) {
	table, err := referenceTableOf(relation)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:name"`
		Slug string `gorm:"column:slug"`
	}
	query := fmt.Sprintf("SELECT reference.id, reference.name, reference.slug FROM %s AS relation JOIN %s AS reference ON reference.id = relation.%s WHERE relation.release_id = ? ORDER BY reference.name ASC, reference.id ASC", table.table, table.reference, table.column)
	if err := s.tx.WithContext(ctx).Raw(query, releaseID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	references := make([]release.Reference, len(rows))
	for i, row := range rows {
		references[i] = release.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug}
	}
	return references, nil
}

func (s *store) Formats(ctx context.Context, releaseID string) ([]release.FormatReference, error) {
	var rows []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        string  `gorm:"column:slug"`
		Description *string `gorm:"column:description"`
	}
	if err := s.tx.WithContext(ctx).Table("release_format AS relation").
		Select("format.id AS id, format.name AS name, format.slug AS slug, relation.format_description AS description").
		Joins("JOIN format ON format.id = relation.format_id").
		Where("relation.release_id = ?", releaseID).
		Order("format.name ASC, format.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	formats := make([]release.FormatReference, len(rows))
	for i, row := range rows {
		formats[i] = release.FormatReference{
			Reference: release.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug}, Description: row.Description,
		}
	}
	return formats, nil
}

func (s *store) EditorCredits(ctx context.Context, releaseID string) ([]release.EditorCredit, error) {
	var rows []struct {
		ID           string  `gorm:"column:id"`
		ArtistID     *string `gorm:"column:artist_id"`
		MemberID     *string `gorm:"column:member_id"`
		MemberName   *string `gorm:"column:member_name"`
		CreditedName *string `gorm:"column:credited_name"`
		CreditRole   *string `gorm:"column:credit_role"`
		SortOrder    int32   `gorm:"column:sort_order"`
	}
	if err := s.tx.WithContext(ctx).Table("release_credit AS credit").
		Select(`credit.id, credit.artist_id, credit.member_id, credit.credited_name, credit.credit_role, credit.sort_order,
			member.nickname AS member_name`).
		Joins("LEFT JOIN member ON member.id = credit.member_id").
		Where("credit.release_id = ?", releaseID).
		Order("credit.sort_order ASC, credit.created_at ASC, credit.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]release.EditorCredit, len(rows))
	for i, row := range rows {
		credits[i] = release.EditorCredit{
			ID: row.ID, ArtistID: row.ArtistID, MemberID: row.MemberID, MemberName: row.MemberName,
			CreditedName: row.CreditedName, CreditRole: row.CreditRole, SortOrder: row.SortOrder,
		}
	}
	return credits, nil
}

func (s *store) ArtistSummaries(ctx context.Context, artistIDs []string) (map[string]release.ArtistSummary, error) {
	return s.ports.Artists.LoadArtistSummariesWithDB(ctx, s.tx, artistIDs)
}

func (s *store) LabelSummaries(ctx context.Context, labelIDs []string) (map[string]release.LabelSummary, error) {
	return s.ports.Labels.LoadLabelSummariesWithDB(ctx, s.tx, labelIDs)
}

func (s *store) LockMemberReferences(ctx context.Context, references []release.MemberReference) error {
	locked := make([]authorizationtarget.Reference, len(references))
	for i, reference := range references {
		locked[i] = authorizationtarget.Reference{MemberID: reference.MemberID, Field: reference.Field}
	}
	return authorizationtarget.LockReferences(ctx, s.tx, locked)
}

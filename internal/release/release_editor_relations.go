package release

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// GetReleaseRelationsQuery names the Release whose relations an editor reads.
type GetReleaseRelationsQuery struct {
	ReleaseID string
}

// GetReleaseRelations returns the current editable relation snapshot after checking
// the same view permission as GetRelease. It is intentionally read-only: relation
// mutations still validate write permission in their own transaction.
func (s *Service) GetReleaseRelations(ctx context.Context, query GetReleaseRelationsQuery) (*managev1.GetReleaseRelationsResponse, error) {
	if err := requireReleaseAction(ctx, s.spiceDB, query.ReleaseID, releaseActionView); err != nil {
		return nil, err
	}
	relations := &managev1.GetReleaseRelationsResponse{ReleaseId: query.ReleaseID}
	err := mutation.Read(ctx, s.db, func(tx *gorm.DB) error {
		var release model.Release
		if err := tx.Select("id").Where("id = ?", query.ReleaseID).Take(&release).Error; err != nil {
			return missingAs(err, releaseNotFound(query.ReleaseID))
		}
		for _, load := range []func(context.Context, *gorm.DB, string, *managev1.GetReleaseRelationsResponse) error{
			s.loadReleaseEditorArtists, s.loadReleaseEditorLabels, loadReleaseEditorReferences, s.loadReleaseEditorCredits,
		} {
			if err := load(ctx, tx, query.ReleaseID, relations); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return relations, nil
}

// optionalSlug is the editor form of a summary slug: absent when empty.
func optionalSlug(slug string) *string {
	if slug == "" {
		return nil
	}
	return &slug
}

func (s *Service) loadReleaseEditorArtists(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	var rows []model.ReleaseArtist
	if err := tx.WithContext(ctx).Where("release_id = ?", releaseID).
		Order("sort_order ASC, artist_id ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("load release artists: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArtistID)
	}
	artists, err := s.artists.LoadArtistSummariesWithDB(ctx, tx, ids)
	if err != nil {
		return fmt.Errorf("load release artists: %w", err)
	}
	response.Artists = make([]*managev1.ReleaseArtistEditorItem, 0, len(rows))
	for _, row := range rows {
		artist, exists := artists[row.ArtistID]
		if !exists {
			continue
		}
		response.Artists = append(response.Artists, &managev1.ReleaseArtistEditorItem{
			ArtistId: row.ArtistID, ArtistName: artist.Title, ArtistSlug: optionalSlug(artist.Slug), SortOrder: int32(row.SortOrder),
		})
	}
	return nil
}

func (s *Service) loadReleaseEditorLabels(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	var rows []model.ReleaseLabel
	if err := tx.WithContext(ctx).Where("release_id = ?", releaseID).
		Order("sort_order ASC, label_id ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("load release labels: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.LabelID)
	}
	labels, err := s.labels.LoadLabelSummariesWithDB(ctx, tx, ids)
	if err != nil {
		return fmt.Errorf("load release labels: %w", err)
	}
	response.Labels = make([]*managev1.ReleaseLabelEditorItem, 0, len(rows))
	for _, row := range rows {
		label, exists := labels[row.LabelID]
		if !exists {
			continue
		}
		response.Labels = append(response.Labels, &managev1.ReleaseLabelEditorItem{
			LabelId: row.LabelID, LabelName: label.Title, LabelSlug: optionalSlug(label.Slug),
			CatalogNumber: row.CatalogNumber, SortOrder: int32(row.SortOrder),
		})
	}
	return nil
}

func loadReleaseEditorReferences(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	response.Categories = make([]*managev1.ReleaseReferenceEditorItem, 0)
	response.Genres = make([]*managev1.ReleaseReferenceEditorItem, 0)
	response.Styles = make([]*managev1.ReleaseReferenceEditorItem, 0)
	response.Formats = make([]*managev1.ReleaseFormatEditorItem, 0)

	for _, relation := range []struct {
		table, key string
		dest       *[]*managev1.ReleaseReferenceEditorItem
	}{{"release_category", "category_id", &response.Categories}, {"release_genre", "genre_id", &response.Genres}, {"release_style", "style_id", &response.Styles}} {
		var rows []struct {
			ID   string `gorm:"column:id"`
			Name string `gorm:"column:name"`
			Slug string `gorm:"column:slug"`
		}
		table := relation.table
		key := relation.key
		query := fmt.Sprintf("SELECT reference.id, reference.name, reference.slug FROM %s AS relation JOIN %s AS reference ON reference.id = relation.%s WHERE relation.release_id = ? ORDER BY reference.name ASC, reference.id ASC", table, tableNameForReleaseReference(table), key)
		if err := tx.WithContext(ctx).Raw(query, releaseID).Scan(&rows).Error; err != nil {
			return fmt.Errorf("load release %s: %w", table, err)
		}
		for i := range rows {
			row := rows[i]
			*relation.dest = append(*relation.dest, &managev1.ReleaseReferenceEditorItem{
				Id: row.ID, Name: row.Name, Slug: row.Slug,
			})
		}
	}

	var formats []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        string  `gorm:"column:slug"`
		Description *string `gorm:"column:description"`
	}
	if err := tx.WithContext(ctx).Table("release_format AS relation").
		Select("format.id AS id, format.name AS name, format.slug AS slug, relation.format_description AS description").
		Joins("JOIN format ON format.id = relation.format_id").
		Where("relation.release_id = ?", releaseID).
		Order("format.name ASC, format.id ASC").Scan(&formats).Error; err != nil {
		return fmt.Errorf("load release formats: %w", err)
	}
	for _, row := range formats {
		response.Formats = append(response.Formats, &managev1.ReleaseFormatEditorItem{
			Id: row.ID, Name: row.Name, Slug: row.Slug, FormatDescription: row.Description,
		})
	}
	return nil
}

func tableNameForReleaseReference(relation string) string {
	switch relation {
	case "release_category":
		return "category"
	case "release_genre":
		return "genre"
	default:
		return "style"
	}
}

func (s *Service) loadReleaseEditorCredits(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	var rows []struct {
		ID           string  `gorm:"column:id"`
		ArtistID     *string `gorm:"column:artist_id"`
		MemberID     *string `gorm:"column:member_id"`
		MemberName   *string `gorm:"column:member_name"`
		CreditedName *string `gorm:"column:credited_name"`
		CreditRole   *string `gorm:"column:credit_role"`
		SortOrder    int32   `gorm:"column:sort_order"`
	}
	err := tx.WithContext(ctx).Table("release_credit AS credit").
		Select(`credit.id, credit.artist_id, credit.member_id, credit.credited_name, credit.credit_role, credit.sort_order,
			member.nickname AS member_name`).
		Joins("LEFT JOIN member ON member.id = credit.member_id").
		Where("credit.release_id = ?", releaseID).
		Order("credit.sort_order ASC, credit.created_at ASC, credit.id ASC").Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load release credits: %w", err)
	}
	artistIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ArtistID != nil {
			artistIDs = append(artistIDs, *row.ArtistID)
		}
	}
	artists, err := s.artists.LoadArtistSummariesWithDB(ctx, tx, artistIDs)
	if err != nil {
		return fmt.Errorf("load release credits: %w", err)
	}
	response.Credits = make([]*managev1.ReleaseCreditEditorItem, 0, len(rows))
	for _, row := range rows {
		credit := &managev1.ReleaseCreditEditorItem{
			Id: row.ID, ArtistId: row.ArtistID, MemberId: row.MemberID, MemberName: row.MemberName,
			CreditedName: row.CreditedName, CreditRole: row.CreditRole, SortOrder: row.SortOrder,
		}
		if row.ArtistID != nil {
			artist := artists[*row.ArtistID]
			credit.ArtistName, credit.ArtistSlug = &artist.Title, optionalSlug(artist.Slug)
		}
		response.Credits = append(response.Credits, credit)
	}
	return nil
}

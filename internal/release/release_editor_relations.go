package release

import (
	"context"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// GetReleaseRelations returns the current editable relation snapshot after checking
// the same view permission as GetRelease. It is intentionally read-only: relation
// mutations still validate write permission in their own transaction.
func (s *ReleaseService) GetReleaseRelations(
	ctx context.Context,
	req *connect.Request[managev1.GetReleaseRelationsRequest],
) (*connect.Response[managev1.GetReleaseRelationsResponse], error) {
	if err := requireReleaseAction(ctx, s.spiceDB, req.Msg.ReleaseId, releaseActionView); err != nil {
		return nil, err
	}

	response := &managev1.GetReleaseRelationsResponse{ReleaseId: req.Msg.ReleaseId}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var release model.Release
		if err := tx.Select("id").Where("id = ?", req.Msg.ReleaseId).Take(&release).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("release", req.Msg.ReleaseId)
			}
			return errs.Internal(err)
		}

		if err := loadReleaseEditorArtists(ctx, tx, req.Msg.ReleaseId, response); err != nil {
			return errs.Internal(err)
		}
		if err := loadReleaseEditorLabels(ctx, tx, req.Msg.ReleaseId, response); err != nil {
			return errs.Internal(err)
		}
		if err := loadReleaseEditorReferences(ctx, tx, req.Msg.ReleaseId, response); err != nil {
			return errs.Internal(err)
		}
		if err := loadReleaseEditorCredits(ctx, tx, req.Msg.ReleaseId, response); err != nil {
			return errs.Internal(err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func loadReleaseEditorArtists(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	var rows []struct {
		ID        string  `gorm:"column:id"`
		Name      string  `gorm:"column:name"`
		Slug      *string `gorm:"column:slug"`
		SortOrder int32   `gorm:"column:sort_order"`
	}
	err := tx.WithContext(ctx).Table("release_artist AS relation").
		Select(`relation.artist_id AS id,
			COALESCE((SELECT translation.title FROM artist_translation AS translation WHERE translation.entity_id = artist.id AND translation.locale = artist.source_locale LIMIT 1), '') AS name,
			artist.slug AS slug, relation.sort_order`).
		Joins("JOIN artist ON artist.id = relation.artist_id").
		Where("relation.release_id = ?", releaseID).
		Order("relation.sort_order ASC, relation.artist_id ASC").Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load release artists: %w", err)
	}
	response.Artists = make([]*managev1.ReleaseArtistEditorItem, 0, len(rows))
	for _, row := range rows {
		response.Artists = append(response.Artists, &managev1.ReleaseArtistEditorItem{
			ArtistId: row.ID, ArtistName: row.Name, ArtistSlug: row.Slug, SortOrder: row.SortOrder,
		})
	}
	return nil
}

func loadReleaseEditorLabels(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	var rows []struct {
		ID            string  `gorm:"column:id"`
		Name          string  `gorm:"column:name"`
		Slug          *string `gorm:"column:slug"`
		CatalogNumber *string `gorm:"column:catalog_number"`
		SortOrder     int32   `gorm:"column:sort_order"`
	}
	err := tx.WithContext(ctx).Table("release_label AS relation").
		Select(`relation.label_id AS id,
			COALESCE((SELECT translation.title FROM label_translation AS translation WHERE translation.entity_id = label.id AND translation.locale = label.source_locale LIMIT 1), '') AS name,
			label.slug AS slug, relation.catalog_number, relation.sort_order`).
		Joins("JOIN label ON label.id = relation.label_id").
		Where("relation.release_id = ?", releaseID).
		Order("relation.sort_order ASC, relation.label_id ASC").Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load release labels: %w", err)
	}
	response.Labels = make([]*managev1.ReleaseLabelEditorItem, 0, len(rows))
	for _, row := range rows {
		response.Labels = append(response.Labels, &managev1.ReleaseLabelEditorItem{
			LabelId: row.ID, LabelName: row.Name, LabelSlug: row.Slug,
			CatalogNumber: row.CatalogNumber, SortOrder: row.SortOrder,
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

func loadReleaseEditorCredits(ctx context.Context, tx *gorm.DB, releaseID string, response *managev1.GetReleaseRelationsResponse) error {
	var rows []struct {
		ID           string  `gorm:"column:id"`
		ArtistID     *string `gorm:"column:artist_id"`
		ArtistName   *string `gorm:"column:artist_name"`
		ArtistSlug   *string `gorm:"column:artist_slug"`
		MemberID     *string `gorm:"column:member_id"`
		MemberName   *string `gorm:"column:member_name"`
		CreditedName *string `gorm:"column:credited_name"`
		CreditRole   *string `gorm:"column:credit_role"`
		SortOrder    int32   `gorm:"column:sort_order"`
	}
	err := tx.WithContext(ctx).Table("release_credit AS credit").
		Select(`credit.id, credit.artist_id, credit.member_id, credit.credited_name, credit.credit_role, credit.sort_order,
			CASE WHEN credit.artist_id IS NOT NULL THEN COALESCE((SELECT translation.title FROM artist_translation AS translation WHERE translation.entity_id = artist.id AND translation.locale = artist.source_locale LIMIT 1), '') ELSE NULL END AS artist_name,
			artist.slug AS artist_slug, member.nickname AS member_name`).
		Joins("LEFT JOIN artist ON artist.id = credit.artist_id").
		Joins("LEFT JOIN member ON member.id = credit.member_id").
		Where("credit.release_id = ?", releaseID).
		Order("credit.sort_order ASC, credit.created_at ASC, credit.id ASC").Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load release credits: %w", err)
	}
	response.Credits = make([]*managev1.ReleaseCreditEditorItem, 0, len(rows))
	for _, row := range rows {
		response.Credits = append(response.Credits, &managev1.ReleaseCreditEditorItem{
			Id: row.ID, ArtistId: row.ArtistID, ArtistName: row.ArtistName, ArtistSlug: row.ArtistSlug,
			MemberId: row.MemberID, MemberName: row.MemberName, CreditedName: row.CreditedName,
			CreditRole: row.CreditRole, SortOrder: row.SortOrder,
		})
	}
	return nil
}

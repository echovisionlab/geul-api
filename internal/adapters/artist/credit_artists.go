package artist

import (
	"context"

	"gorm.io/gorm"
)

// CreditArtists reads the Artist-owned projection a credit line shows: the
// artist's source-locale name, its slug and its first image File.
type CreditArtists struct{}

// CreditArtist returns the ID, source-locale name and slug of the artist
// artistID names in db; an empty ID means it does not exist.
func (CreditArtists) CreditArtist(ctx context.Context, db *gorm.DB, artistID string) (string, string, *string, error) {
	var artist struct {
		ID   string  `gorm:"column:id"`
		Name string  `gorm:"column:name"`
		Slug *string `gorm:"column:slug"`
	}
	err := db.WithContext(ctx).
		Table("artist").
		Select(`artist.id, COALESCE((SELECT translation.title FROM artist_translation AS translation
			WHERE translation.entity_id = artist.id AND translation.locale = artist.source_locale LIMIT 1), '') AS name,
			artist.slug`).
		Where("artist.id = ?", artistID).
		Scan(&artist).Error
	return artist.ID, artist.Name, artist.Slug, err
}

// FirstImageFileID returns the File of the artist's first image, or empty.
func (CreditArtists) FirstImageFileID(ctx context.Context, db *gorm.DB, artistID string) (string, error) {
	var result struct {
		FileID string `gorm:"column:file_id"`
	}
	err := db.WithContext(ctx).
		Table("artist_file").
		Select("file_id").
		Where("artist_file.artist_id = ?", artistID).
		Order("artist_file.sort_order ASC").
		Limit(1).
		Scan(&result).Error
	return result.FileID, err
}

package post

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// TranslationInterchangeDocumentID reads the Content Document of a Post for
// its XLIFF interchange; a missing Post answers gorm.ErrRecordNotFound.
func TranslationInterchangeDocumentID(ctx context.Context, db *gorm.DB, postID string) (*string, error) {
	var root struct {
		ContentDocumentID *string `gorm:"column:content_document_id"`
	}
	err := db.WithContext(ctx).Table("post").Select("content_document_id").
		Where("id = ?", postID).Take(&root).Error
	return root.ContentDocumentID, err
}

// TranslationInterchangeLocale is the stored title and summary of one Post
// locale.
type TranslationInterchangeLocale struct {
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TranslationInterchangeLocaleRow reads the Post's locale row and reports
// whether it exists.
func TranslationInterchangeLocaleRow(ctx context.Context, db *gorm.DB, postID, locale string) (TranslationInterchangeLocale, bool, error) {
	var row TranslationInterchangeLocale
	err := db.WithContext(ctx).Table("post_translation").Select("title", "summary", "updated_at").
		Where("entity_id = ? AND locale = ?", postID, locale).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return TranslationInterchangeLocale{}, false, nil
	}
	return row, err == nil, err
}

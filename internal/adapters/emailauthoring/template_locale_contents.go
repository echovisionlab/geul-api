package emailauthoring

import (
	"context"

	"gorm.io/gorm"
)

// TemplateLocaleContent is one locale's subject and HTML of an Email
// Template.
type TemplateLocaleContent struct {
	Locale         string  `gorm:"column:locale"`
	Subject        *string `gorm:"column:subject"`
	ContentHTML    *string `gorm:"column:content_html"`
	IsSourceLocale bool    `gorm:"column:is_source_locale"`
}

// TemplateLocaleContents reads every locale of the Email Template, its
// source locale first and the others by locale.
func TemplateLocaleContents(ctx context.Context, db *gorm.DB, templateID string) ([]TemplateLocaleContent, error) {
	var rows []TemplateLocaleContent
	err := db.WithContext(ctx).Table("email_template_translation AS translation").
		Select("translation.locale, translation.subject, translation.content_html, translation.locale = source.source_locale AS is_source_locale").
		Joins("JOIN email_template AS source ON source.id = translation.entity_id").
		Where("translation.entity_id = ?", templateID).
		Order("is_source_locale DESC, locale ASC").Scan(&rows).Error
	return rows, err
}

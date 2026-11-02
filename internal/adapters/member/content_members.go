package memberadapter

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
)

// ContentMembers is the Member-owned surface a content domain reads: the
// preferred locale that seeds a new document's source locale, and the
// existence lock of the Members a collaboration write is attributed to.
type ContentMembers struct{}

// PreferredLocale returns the stored preferred locale of the live Member
// memberID, or nil.
func (ContentMembers) PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error) {
	var preferredLocale *string
	err := db.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", memberID).
		Scan(&preferredLocale).Error
	return preferredLocale, err
}

// LockMembers takes a key-share lock on the Member rows memberIDs name and
// returns how many exist.
func (ContentMembers) LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) (int, error) {
	var members []struct {
		ID string `gorm:"column:id"`
	}
	err := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", memberIDs).
		Find(&members).Error
	return len(members), err
}

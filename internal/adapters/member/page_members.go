package memberadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PageMembers reads the Member and user tag rows Page authorization and
// attribution need.
type PageMembers struct{}

// LockMembers takes a KEY SHARE lock on each existing Member of memberIDs,
// so a concurrent Member deletion waits for the write, and returns the IDs
// it locked.
func (PageMembers) LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error) {
	locked := []string{}
	err := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Where("id IN ?", memberIDs).
		Pluck("id::text", &locked).Error
	return locked, err
}

// ActiveIdentities returns the linked Identity of each onboarded, live
// Member of memberIDs whose Identity is active and not banned.
func (PageMembers) ActiveIdentities(ctx context.Context, tx *gorm.DB, memberIDs []string) (map[string]string, error) {
	var rows []struct {
		MemberID   string `gorm:"column:member_id"`
		IdentityID string `gorm:"column:identity_id"`
	}
	if err := tx.WithContext(ctx).
		Table("member AS member").
		Joins(identityread.JoinLinked("member", "identity")).
		Select("member.id::text AS member_id, member.account_identity_id::text AS identity_id").
		Where("member.id IN ? AND member.deleted_at IS NULL AND member.onboarded = TRUE AND member.account_identity_id IS NOT NULL AND NOT "+identityread.BannedSQL("identity"), memberIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	identities := make(map[string]string, len(rows))
	for _, row := range rows {
		identities[row.MemberID] = row.IdentityID
	}
	return identities, nil
}

// Nicknames returns the nickname of each existing Member of memberIDs.
func (PageMembers) Nicknames(ctx context.Context, db *gorm.DB, memberIDs []string) (map[string]string, error) {
	var members []model.Member
	if err := db.WithContext(ctx).Select("id", "nickname").Where("id IN ?", memberIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	nicknames := make(map[string]string, len(members))
	for _, member := range members {
		nicknames[member.ID] = member.Nickname
	}
	return nicknames, nil
}

// PreferredLocale returns the preferred locale of a live Member, or nil.
func (PageMembers) PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error) {
	var preferredLocale *string
	err := db.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").Where("id = ?::uuid AND deleted_at IS NULL", memberID).
		Scan(&preferredLocale).Error
	return preferredLocale, err
}

// LockUserTags takes a SHARE lock on each existing user tag of tagIDs and
// returns the IDs it locked.
func (PageMembers) LockUserTags(ctx context.Context, tx *gorm.DB, tagIDs []string) ([]string, error) {
	var ids []string
	err := tx.WithContext(ctx).Model(&model.UserTag{}).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id IN ?", tagIDs).Pluck("id", &ids).Error
	return ids, err
}

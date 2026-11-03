// Package oauthapp contains the OAuth App-owned surfaces other domains read.
package oauthapp

import (
	"context"

	"gorm.io/gorm"
)

// SignInAppUsage counts the sign-in apps that admit Members through each
// Member criteria (criteriapostgres.SignInAppUsage).
type SignInAppUsage struct{}

func (SignInAppUsage) CountSignInApps(ctx context.Context, db *gorm.DB, criteriaIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32, len(criteriaIDs))
	if len(criteriaIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT member_criteria_id::text AS id, COUNT(*) AS count
		FROM oauth_sign_in_app_member_criteria
		WHERE member_criteria_id IN ?
		GROUP BY member_criteria_id
	`, criteriaIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.ID] = int32(row.Count)
	}
	return counts, nil
}

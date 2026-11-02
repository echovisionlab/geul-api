package legalpostgres

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// ScheduledVersionIDs reads policies eligible for the activation scheduler.
func ScheduledVersionIDs(ctx context.Context, db *gorm.DB, kind legal.PolicyKind) ([]string, error) {
	var history any
	var status model.LegalPolicyStatus
	switch kind {
	case legal.Terms:
		history, status = &model.Terms{}, model.TermsStatusScheduled
	case legal.Privacy:
		history, status = &model.Privacy{}, model.PrivacyStatusScheduled
	default:
		return nil, fmt.Errorf("unknown legal policy kind %q", kind)
	}
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).Model(history).Select("id").Where("status = ?", status).Order("effective_from ASC, version ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

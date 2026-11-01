package workerpostgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/worker"
)

// legalPolicies is the Legal port outside an activation transaction.
type legalPolicies struct {
	db      *gorm.DB
	notices legaladapter.EmailDeliveryDispatcher
}

// legalPolicyKind is Legal's name of kind; the two share their values.
func legalPolicyKind(kind worker.LegalKind) legaldomain.PolicyKind {
	return legaldomain.PolicyKind(kind)
}

func (p legalPolicies) ScheduledVersions(ctx context.Context, kind worker.LegalKind) ([]string, error) {
	var (
		history         structured.Value
		scheduledStatus model.LegalPolicyStatus
	)
	switch kind {
	case worker.LegalTerms:
		history, scheduledStatus = &model.Terms{}, model.TermsStatusScheduled
	case worker.LegalPrivacy:
		history, scheduledStatus = &model.Privacy{}, model.PrivacyStatusScheduled
	default:
		return nil, fmt.Errorf("unknown legal policy kind %q", kind)
	}
	var pending []struct {
		ID string `gorm:"column:id"`
	}
	if err := p.db.WithContext(ctx).
		Model(history).
		Select("id").
		Where("status = ?", scheduledStatus).
		Order("effective_from ASC, version ASC, id ASC").
		Find(&pending).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(pending))
	for _, version := range pending {
		ids = append(ids, version.ID)
	}
	return ids, nil
}

func (p legalPolicies) DispatchEffectiveNotice(ctx context.Context, kind worker.LegalKind, documentID string, now time.Time, notice worker.EffectiveNotice) {
	run, _ := notice.(*legaldomain.NoticeRun)
	legaldomain.DispatchCommittedEffectiveNotice(
		ctx,
		legaladapter.NewNoticeDispatch(p.db, p.notices),
		legalPolicyKind(kind),
		documentID,
		now,
		run,
	)
}

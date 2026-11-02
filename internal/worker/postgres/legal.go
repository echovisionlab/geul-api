package workerpostgres

import (
	"context"
	"time"

	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"

	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
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
	return legalpostgres.ScheduledVersionIDs(ctx, p.db, legalPolicyKind(kind))
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

package menuadapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type transactionRecorder struct{ transactions []*gorm.DB }

func (r *transactionRecorder) AppendDomainAuditInTransaction(_ context.Context, tx *gorm.DB, _ sharedtelemetry.AuditRecord) error {
	r.transactions = append(r.transactions, tx)
	return nil
}

func TestJoinAppendsInTheJoinedTransaction(t *testing.T) {
	tx := &gorm.DB{}
	recorder := &transactionRecorder{}

	joined := Join(tx, recorder)
	require.NoError(t, joined.Audit.AppendDomainAuditInTransaction(t.Context(), nil, sharedtelemetry.AuditRecord{}))

	require.Equal(t, []*gorm.DB{tx}, recorder.transactions)
	require.Nil(t, Join(tx, nil).Audit, "an unaudited caller stays unaudited")
}

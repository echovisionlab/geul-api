package menupostgres_test

import (
	"context"
	"testing"

	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type transactionRecorder struct{ transactions []*gorm.DB }

func (r *transactionRecorder) AppendDomainAuditInTransaction(_ context.Context, tx *gorm.DB, _ sharedtelemetry.AuditRecord) error {
	r.transactions = append(r.transactions, tx)
	return nil
}

func TestBindAppendsInTheJoinedTransaction(t *testing.T) {
	tx := &gorm.DB{}
	recorder := &transactionRecorder{}

	joined := menupostgres.Bind(tx, menupostgres.Ports{}, recorder)
	require.NoError(t, joined.Audit.AppendDomainAuditInTransaction(t.Context(), nil, sharedtelemetry.AuditRecord{}))

	require.Equal(t, []*gorm.DB{tx}, recorder.transactions)
	require.Nil(t, menupostgres.Bind(tx, menupostgres.Ports{}, nil).Audit, "an unaudited caller stays unaudited")
}

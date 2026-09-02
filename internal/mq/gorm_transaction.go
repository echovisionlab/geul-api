package mq

import (
	"fmt"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"gorm.io/gorm"
)

// GormTransactionExecutor exposes the SQL executor owned by a live GORM
// transaction so a PGMQ command can commit atomically with domain state.
func GormTransactionExecutor(tx *gorm.DB) (eventpkg.DBTX, error) {
	if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return nil, fmt.Errorf("database transaction is required")
	}
	executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
	if !ok {
		return nil, fmt.Errorf("database transaction does not expose a PGMQ executor")
	}
	return executor, nil
}

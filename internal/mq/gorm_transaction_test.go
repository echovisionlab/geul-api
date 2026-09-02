package mq

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGormTransactionExecutorRequiresLiveTransaction(t *testing.T) {
	_, err := GormTransactionExecutor(nil)
	require.ErrorContains(t, err, "database transaction is required")

	_, err = GormTransactionExecutor(&gorm.DB{})
	require.ErrorContains(t, err, "database transaction is required")
}

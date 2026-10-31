package mutation

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLockingReadSeesOneSnapshot(t *testing.T) {
	_, db := newTestRunner(t, &audittest.Recorder{})
	require.NoError(t, db.Create(&note{Text: "stored"}).Error)
	var read []note
	require.NoError(t, LockingRead(context.Background(), db, func(tx *gorm.DB) error { return tx.Find(&read).Error }))
	require.Len(t, read, 1)
	readErr := errors.New("read failed")
	require.ErrorIs(t, LockingRead(context.Background(), db, func(*gorm.DB) error { return readErr }), readErr)
}

package mutation

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLockedReadsReturnWhatTheyRead(t *testing.T) {
	_, db := newTestRunner(t, &audittest.Recorder{})
	require.NoError(t, db.Create(&note{Text: "stored"}).Error)
	readErr := errors.New("read failed")
	for name, read := range map[string]func(context.Context, *gorm.DB, func(*gorm.DB) error) error{
		"locked snapshot": ReadLocked,
		"current":         ReadCurrent,
	} {
		t.Run(name, func(t *testing.T) {
			var notes []note
			require.NoError(t, read(context.Background(), db, func(tx *gorm.DB) error { return tx.Find(&notes).Error }))
			require.Len(t, notes, 1)
			require.ErrorIs(t, read(context.Background(), db, func(*gorm.DB) error { return readErr }), readErr)
		})
	}
}

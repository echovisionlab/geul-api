package mutation

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"

	"gorm.io/gorm"
)

var optionalReadSequence atomic.Uint64

// BestEffortRead isolates an optional lookup so its SQL error does not abort
// the caller's transaction. The caller receives the error and chooses a fallback.
func BestEffortRead[T any](ctx context.Context, tx *gorm.DB, read func(*gorm.DB) (T, error)) (value T, err error) {
	name := "optional_read_" + strconv.FormatUint(optionalReadSequence.Add(1), 10)
	db := tx.WithContext(ctx)
	if err = db.SavePoint(name).Error; err != nil {
		return value, err
	}
	value, err = read(db)
	if err != nil {
		err = errors.Join(err, db.RollbackTo(name).Error)
	}
	return value, errors.Join(err, db.Exec("RELEASE SAVEPOINT "+name).Error)
}

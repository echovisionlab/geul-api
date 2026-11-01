// Package filemediapostgres implements the File Store against PostgreSQL.
package filemediapostgres

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"gorm.io/gorm"
)

type Ports struct{}
type store struct{ tx *gorm.DB }

func NewUnits(db *gorm.DB, ports Ports) mutation.UnitOfWork[domain.Store] {
	if db == nil {
		panic("file media units: db is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) domain.Store { return Bind(tx, ports) })
}
func Bind(tx *gorm.DB, _ Ports) domain.Store { return &store{tx: tx} }

type storeError struct {
	error
	kind error
}

func (e storeError) Is(target error) bool { return target == e.kind }
func (e storeError) Unwrap() error        { return e.error }

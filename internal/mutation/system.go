package mutation

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"gorm.io/gorm"
)

// NewSystemStoreRunner runs trusted service work without a request principal.
// It requires System access and system audit records, and rejects Identity
// subjects and SpiceDB relationship batches.
func NewSystemStoreRunner[S any](units UnitOfWork[S], audit domainaudit.Appender) (*StoreRunner[S], error) {
	if units == nil {
		return nil, errors.New("mutation unit of work is required")
	}
	if audit == nil {
		return nil, errors.New("mutation audit appender is required")
	}
	return &StoreRunner[S]{units: units, audit: audit}, nil
}

// System admits a mutation initiated by a trusted internal service boundary.
func System() Access {
	return Access{system: true, check: func(context.Context, *gorm.DB, any, authority) error { return nil }}
}

func (a Audit) systemOnly() bool {
	if a.unchanged {
		return true
	}
	if len(a.all) != 0 {
		for _, each := range a.all {
			if !each.systemOnly() {
				return false
			}
		}
		return true
	}
	return a.service != ""
}

package referencecatalog

import (
	"context"

	"gorm.io/gorm"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// MemberSummaries resolves the member projections displayed by Map Place.
type MemberSummaries interface {
	Resolve(context.Context, *gorm.DB, []string) (map[string]*commonv1.MemberSummary, error)
}

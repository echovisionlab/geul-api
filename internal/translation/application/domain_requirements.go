package application

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"gorm.io/gorm"
)

func requireEditableTranslationDomain(
	ctx context.Context,
	db *gorm.DB,
	domains DomainRegistry,
	entityType string,
	entityID string,
) error {
	if domains == nil {
		return errs.InternalMsg("translation domain registry is required")
	}
	return domains.RequireEditable(ctx, db, entityType, entityID)
}

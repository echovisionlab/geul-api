package post

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// loadPostBlockMedia runs only inside the caller's authorized owner transaction.
// Durable references are never returned in place of request-scoped delivery.
func loadPostBlockMedia(ctx context.Context, tx *gorm.DB, postID string, documentID uuid.UUID, principal *auth.UserInfo, loader ContentBlockMediaLoader, hydrator ContentBlockMediaHydrator) ([]*contentv1.ContentBlockMediaItem, error) {
	if hydrator == nil {
		return nil, errs.InternalMsg("Post Block media hydrator is not configured")
	}
	items, err := loader.LoadContentBlockMediaReferences(ctx, tx, documentID)
	if err != nil {
		return nil, err
	}
	return hydrator.HydrateAuthorizedPostBlockMediaWithDB(auth.WithUser(ctx, principal), tx, postID, documentID, principal, items)
}

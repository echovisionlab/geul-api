//go:build integration

package integration

import (
	"context"
	"testing"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type extractedWorkPublicMediaHydrator struct {
}

func (h extractedWorkPublicMediaHydrator) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

func (h extractedWorkPublicMediaHydrator) HydrateAuthorizedWorkBlockMediaWithDB(
	ctx context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return h.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func newExtractedPublicWorkService(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
) *workpublic.WorkService {
	t.Helper()
	return workpublic.NewWorkService(
		db,
		publicIntegrationSpiceDB,
		extractedWorkPublicMediaHydrator{},
		newPublicWorkRuntimeForTest(db, cdnDomain),
		workadapter.NewMemberSummaries(db, cdnDomain),
		referencecatalogadapter.PublicMapPlaces{},
		workpublic.WithWorkContentBlockStore(newPublicWorkContentBlockStore(t)),
	)
}

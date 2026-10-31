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
	"github.com/stretchr/testify/require"
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
) *workpublic.ConnectHandler {
	t.Helper()
	return newPublicWorkHandler(t, workpublic.Deps{
		DB: db, SpiceDB: publicIntegrationSpiceDBFor(t, db), BlockMedia: extractedWorkPublicMediaHydrator{},
		Assets: newPublicWorkRuntimeForTest(db, cdnDomain), Members: workadapter.NewMemberSummaries(db, cdnDomain),
	})
}

// newPublicWorkHandler builds the public Work reads, filling the
// collaborators deps leaves unset.
func newPublicWorkHandler(t *testing.T, deps workpublic.Deps) *workpublic.ConnectHandler {
	t.Helper()
	if deps.MapPlaces == nil {
		deps.MapPlaces = referencecatalogadapter.PublicMapPlaces{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = newPublicWorkContentBlockStore(t)
	}
	service, err := workpublic.NewService(deps)
	require.NoError(t, err)
	return workpublic.NewConnectHandler(service)
}

//go:build integration

package integration

import (
	"context"
	"testing"

	"gorm.io/gorm"

	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newWorkIntegrationService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	_ any,
) *workdomain.WorkService {
	t.Helper()
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	store, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	return workdomain.NewWorkService(
		db,
		newWorkRuntimeForTest(db, ""),
		spiceDB,
		&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")},
		noopAsyncPublisher{},
		workdomain.WithWorkContentBlockStore(store),
		workdomain.WithWorkContentBlockMediaHydrator(passthroughWorkContentBlockMediaHydrator{}),
		workdomain.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, "")),
	)
}

type passthroughWorkContentBlockMediaHydrator struct{}

func (passthroughWorkContentBlockMediaHydrator) HydrateAuthorizedContentBlockMedia(
	_ context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

func (passthroughWorkContentBlockMediaHydrator) HydrateAuthorizedWorkBlockMediaWithDB(
	_ context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

func emptyWorkIntegrationDocument(locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		SourceLocale:            locale,
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: locale,
		}},
	}
}

func workIntegrationAdminCtx(id string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(integrationMemberID(id)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
		Onboarded:     true,
	})
}

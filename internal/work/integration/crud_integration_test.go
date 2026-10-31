//go:build integration

package integration

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

func newWorkIntegrationService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	_ any,
) *workdomain.ConnectHandler {
	t.Helper()
	spiceDB := integrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	return workdomain.NewConnectHandler(newWorkServiceForTest(t, workdomain.Deps{DB: db, SpiceDB: spiceDB}))
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

// workIntegrationAdminCtx is an administrator's request, so mutations can
// name their audit records.
func workIntegrationAdminCtx(id string) context.Context {
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	if err != nil {
		panic(err)
	}
	return auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(testutil.PostIntegrationMemberID(id)),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
		Onboarded:     true,
	})
}

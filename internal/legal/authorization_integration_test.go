//go:build integration

package legal_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestLegalAuthorizationCreateDeleteIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Site legal admin")
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	store, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	writer := apitelemetry.NewDurableWriter(db)

	termsService := legaldomain.NewAuditedTermsService(
		db, "https://example.test", writer, spiceDB,
		legalIntegrationDependencies(db, ""),
		legaldomain.WithTermsContentBlockStore(store),
	)
	privacyService := legaldomain.NewAuditedPrivacyService(
		db, "https://example.test", writer, spiceDB,
		legalIntegrationDependencies(db, ""),
		legaldomain.WithPrivacyContentBlockStore(store),
	)

	terms, err := termsService.CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{
		Document: legalPolicyDocumentFixture("en", "authorization terms"),
	}))
	require.NoError(t, err)
	privacy, err := privacyService.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
		Document: legalPolicyDocumentFixture("en", "authorization privacy"),
	}))
	require.NoError(t, err)

	for _, resource := range []struct {
		manage func(string) (policyv1.Can, error)
		id     string
	}{
		{policyv1.TermsHistory.Manage, terms.Msg.Id},
		{policyv1.PrivacyHistory.Manage, privacy.Msg.Id},
	} {
		testutil.RequireSynchronousResourceAuthorization(t, spiceDB, resource.manage, resource.id, identityID, true)
	}

	_, err = termsService.DeleteTerms(ctx, connect.NewRequest(&managev1.DeleteTermsRequest{
		Id: terms.Msg.Id, ExpectedRevision: terms.Msg.Revision,
	}))
	require.NoError(t, err)
	_, err = privacyService.DeletePrivacy(ctx, connect.NewRequest(&managev1.DeletePrivacyRequest{
		Id: privacy.Msg.Id, ExpectedRevision: privacy.Msg.Revision,
	}))
	require.NoError(t, err)

	for _, resource := range []struct {
		manage func(string) (policyv1.Can, error)
		id     string
	}{
		{policyv1.TermsHistory.Manage, terms.Msg.Id},
		{policyv1.PrivacyHistory.Manage, privacy.Msg.Id},
	} {
		testutil.RequireSynchronousResourceAuthorization(t, spiceDB, resource.manage, resource.id, identityID, false)
	}
}

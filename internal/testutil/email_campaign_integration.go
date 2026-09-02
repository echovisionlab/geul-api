//go:build integration

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func NewIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func IntegrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return SetupOryStack(t).SpiceDBClient
}

func GrantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func NewAuditContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{IdentityID: identityID, MemberID: memberID, SessionID: uuid.NewString()})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true})
}

func IntegrationUUID() string { return uuid.NewString() }

func InsertDocumentContributor(t *testing.T, db *gorm.DB, memberID string) {
	t.Helper()
	email := memberID + "@source-revision.test"
	require.NoError(t, db.Exec(`INSERT INTO public.member (id, nickname, onboarded, primary_email, available_emails) VALUES (?::uuid, ?, true, ?, ARRAY[?]::text[])`, memberID, "source-revision-"+memberID, email, email).Error)
}

func InsertAuthorizedDocumentContributor(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	memberID string,
) {
	t.Helper()
	identityID := uuid.NewString()
	email := identityID + "@authorized-contributor.test"
	SeedKratosIdentityFixture(t, db, KratosIdentityFixture{
		ID: identityID, Email: email, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
			memberID, identityID,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
			ON CONFLICT (id) DO NOTHING`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{
			ID: memberID, AccountIdentityID: &identityID,
			Nickname: "Authorized contributor " + memberID, Onboarded: true,
			PrimaryEmail: &email, AvailableEmails: []string{email},
			SocialLinks: map[string]string{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}).Error
	}))
	GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?", identityID).Error)
	})
}

func IntegrationAdminContext(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	identityID := uuid.NewString()
	email := "integration-admin-" + identityID + "@example.test"
	SeedKratosIdentityFixture(t, db, KratosIdentityFixture{ID: identityID, Email: email, CreatedAt: time.Now().UTC()})
	memberID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id, created_at) SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid ON CONFLICT (id) DO NOTHING`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: "Email fixture " + memberID, Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error
	}))
	t.Cleanup(func() { require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?", identityID).Error) })
	spiceDB := IntegrationSpiceDB(t)
	GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}), spiceDB
}

type emailCampaignNoFileReuseAuthorizer struct{}

func (emailCampaignNoFileReuseAuthorizer) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

func NewEmailContentBlockStore(t *testing.T, _ *auth.SpiceDBClient) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(emailCampaignNoFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func NewParagraphBatch(document *contentv1.RichTextDocument, expectedRevision, locale, text string, contributors []string) *contentv1.RichTextBlockMutationBatch {
	blockID := uuid.NewString()
	return &contentv1.RichTextBlockMutationBatch{BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(), Profile: document.GetProfile(), ExpectedRevision: expectedRevision, ContributorMemberIds: contributors, BaseMutations: []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{Node: &contentv1.RichTextBlockNode{Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 0}}}}}}, LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: locale, Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}}}}}}}}}}}}
}

func RequireSynchronousResourceAuthorization(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	manage func(string) (policyv1.Can, error),
	resourceID string,
	identityID string,
	want bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := manage(resourceID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed)
}

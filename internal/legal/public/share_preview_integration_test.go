//go:build integration

package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestPublicLegalSharePreviewRequiresExactIDTokenPairIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	store := newPublicLegalContentBlockStore(t)
	media := legaladapter.NewOGRuntime("", nil)
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	future := now.Add(2 * time.Hour)

	t.Run("privacy", func(t *testing.T) {
		archived := seedPublicPrivacyPolicy(
			t, db, store, 910001, "Archived privacy", "archived privacy",
			managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(), &past, &past,
		)
		scheduled := seedPublicPrivacyPolicy(
			t, db, store, 910002, "Scheduled privacy", "exact privacy preview",
			managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), &future, nil,
		)
		link := model.ShareLink{
			ID: uuid.NewString(), Token: "privacy-" + uuid.NewString(),
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
			EntityID:   scheduled.ID, ExpiresAt: &future,
		}
		require.NoError(t, db.Create(&link).Error)

		service := legalpublic.NewPrivacyServiceWithContentBlocks(db, store, media)
		preview, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetPrivacyRequest{
			Id: &scheduled.ID, ShareToken: &link.Token,
		}))
		require.NoError(t, err)
		require.Nil(t, preview.Msg.Privacy)
		require.Equal(t, scheduled.ID, preview.Msg.Scheduled.GetId())
		require.Equal(t, "exact privacy preview", publicLegalDocumentText(preview.Msg.Scheduled.GetDocument()))

		password := "privacy-preview-password"
		passwordHash, err := crypto.NewPasswordHasher(nil).Hash(password)
		require.NoError(t, err)
		require.NoError(t, db.Model(&link).Update("password_hash", passwordHash).Error)
		for _, supplied := range []string{"", "wrong-password"} {
			protected, getErr := service.Get(context.Background(), connect.NewRequest(&openv1.GetPrivacyRequest{
				Id: &scheduled.ID, ShareToken: &link.Token, SharePassword: &supplied,
			}))
			require.NoError(t, getErr)
			require.Nil(t, protected.Msg.Scheduled)
		}
		protected, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetPrivacyRequest{
			Id: &scheduled.ID, ShareToken: &link.Token, SharePassword: &password,
		}))
		require.NoError(t, err)
		require.Equal(t, scheduled.ID, protected.Msg.Scheduled.GetId())

		mismatched, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetPrivacyRequest{
			Id: &archived.ID, ShareToken: &link.Token,
		}))
		require.NoError(t, err)
		require.Nil(t, mismatched.Msg.Scheduled)
		tokenOnly, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetPrivacyRequest{
			ShareToken: &link.Token,
		}))
		require.NoError(t, err)
		require.Nil(t, tokenOnly.Msg.Scheduled)
	})

	t.Run("terms", func(t *testing.T) {
		archived := seedPublicTermsPolicy(
			t, db, store, 920001, "Archived terms", "archived terms",
			managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(), &past, &past,
		)
		scheduled := seedPublicTermsPolicy(
			t, db, store, 920002, "Scheduled terms", "exact terms preview",
			managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(), &future, nil,
		)
		link := model.ShareLink{
			ID: uuid.NewString(), Token: "terms-" + uuid.NewString(),
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(),
			EntityID:   scheduled.ID, ExpiresAt: &future,
		}
		require.NoError(t, db.Create(&link).Error)

		service := legalpublic.NewTermsServiceWithContentBlocks(db, store, media)
		preview, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetTermsRequest{
			Id: &scheduled.ID, ShareToken: &link.Token,
		}))
		require.NoError(t, err)
		require.Nil(t, preview.Msg.Terms)
		require.Equal(t, scheduled.ID, preview.Msg.Scheduled.GetId())
		require.Equal(t, "exact terms preview", publicLegalDocumentText(preview.Msg.Scheduled.GetDocument()))

		password := "terms-preview-password"
		passwordHash, err := crypto.NewPasswordHasher(nil).Hash(password)
		require.NoError(t, err)
		require.NoError(t, db.Model(&link).Update("password_hash", passwordHash).Error)
		for _, supplied := range []string{"", "wrong-password"} {
			protected, getErr := service.Get(context.Background(), connect.NewRequest(&openv1.GetTermsRequest{
				Id: &scheduled.ID, ShareToken: &link.Token, SharePassword: &supplied,
			}))
			require.NoError(t, getErr)
			require.Nil(t, protected.Msg.Scheduled)
		}
		protected, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetTermsRequest{
			Id: &scheduled.ID, ShareToken: &link.Token, SharePassword: &password,
		}))
		require.NoError(t, err)
		require.Equal(t, scheduled.ID, protected.Msg.Scheduled.GetId())

		mismatched, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetTermsRequest{
			Id: &archived.ID, ShareToken: &link.Token,
		}))
		require.NoError(t, err)
		require.Nil(t, mismatched.Msg.Scheduled)
		tokenOnly, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetTermsRequest{
			ShareToken: &link.Token,
		}))
		require.NoError(t, err)
		require.Nil(t, tokenOnly.Msg.Scheduled)
	})
}

func TestPublicLegalManualShareLinkOpensExactTypedHistoryAfterPasswordIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	store := newPublicLegalContentBlockStore(t)
	media := legaladapter.NewOGRuntime("", nil)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	password := "public-history-password"
	passwordHash, err := crypto.NewPasswordHasher(nil).Hash(password)
	require.NoError(t, err)

	privacy := seedPublicPrivacyPolicy(
		t, db, store, 930001, "Active privacy", "Exact active privacy",
		managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(), &past, nil,
	)
	terms := seedPublicTermsPolicy(
		t, db, store, 940001, "Archived terms", "Exact archived terms",
		managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(), &past, &past,
	)
	privacyLink := model.ShareLink{
		ID: uuid.NewString(), Token: "active-privacy-" + uuid.NewString(),
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
		EntityID:   privacy.ID, PasswordHash: &passwordHash, ExpiresAt: &future,
	}
	termsLink := model.ShareLink{
		ID: uuid.NewString(), Token: "archived-terms-" + uuid.NewString(),
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(),
		EntityID:   terms.ID, PasswordHash: &passwordHash, ExpiresAt: &future,
	}
	require.NoError(t, db.Create(&privacyLink).Error)
	require.NoError(t, db.Create(&termsLink).Error)

	wrong := "wrong"
	privacyService := legalpublic.NewPrivacyServiceWithContentBlocks(db, store, media)
	rejectedPrivacy, err := privacyService.Get(t.Context(), connect.NewRequest(&openv1.GetPrivacyRequest{
		Id: &privacy.ID, ShareToken: &privacyLink.Token, SharePassword: &wrong,
	}))
	require.NoError(t, err)
	require.Nil(t, rejectedPrivacy.Msg.Privacy)
	require.Nil(t, rejectedPrivacy.Msg.Scheduled)
	openedPrivacy, err := privacyService.Get(t.Context(), connect.NewRequest(&openv1.GetPrivacyRequest{
		Id: &privacy.ID, ShareToken: &privacyLink.Token, SharePassword: &password,
	}))
	require.NoError(t, err)
	require.Equal(t, privacy.ID, openedPrivacy.Msg.Privacy.GetId())
	require.Equal(t, "Exact active privacy", publicLegalDocumentText(openedPrivacy.Msg.Privacy.GetDocument()))
	require.Nil(t, openedPrivacy.Msg.Scheduled)

	termsService := legalpublic.NewTermsServiceWithContentBlocks(db, store, media)
	openedTerms, err := termsService.Get(t.Context(), connect.NewRequest(&openv1.GetTermsRequest{
		Id: &terms.ID, ShareToken: &termsLink.Token, SharePassword: &password,
	}))
	require.NoError(t, err)
	require.Equal(t, terms.ID, openedTerms.Msg.Terms.GetId())
	require.Equal(t, "Exact archived terms", publicLegalDocumentText(openedTerms.Msg.Terms.GetDocument()))
	require.Nil(t, openedTerms.Msg.Scheduled)

	expiredAt := now.Add(-time.Minute)
	require.NoError(t, db.Model(&termsLink).Updates(structured.Fields{
		"created_at": now.Add(-2 * time.Hour), "expires_at": expiredAt,
	}).Error)
	expiredTerms, err := termsService.Get(t.Context(), connect.NewRequest(&openv1.GetTermsRequest{
		Id: &terms.ID, ShareToken: &termsLink.Token, SharePassword: &password,
	}))
	require.NoError(t, err)
	require.Nil(t, expiredTerms.Msg.Terms)
	require.Nil(t, expiredTerms.Msg.Scheduled)
}

type publicLegalFileReuseAuthorizer struct{}

func (publicLegalFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func newPublicLegalContentBlockStore(t *testing.T) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(publicLegalFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func seedPublicPrivacyPolicy(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	version int,
	title string,
	body string,
	status string,
	effectiveFrom *time.Time,
	effectiveUntil *time.Time,
) model.Privacy {
	t.Helper()
	row := model.Privacy{
		ID: uuid.NewString(), Version: version, Title: title, Content: "", Status: status,
		EffectiveFrom: effectiveFrom, EffectiveUntil: effectiveUntil,
	}
	seedPublicLegalContentDocument(t, db, store, "privacy", row.ID, title, body, &row.ContentDocumentID, &row)
	return row
}

func seedPublicTermsPolicy(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	version int,
	title string,
	body string,
	status string,
	effectiveFrom *time.Time,
	effectiveUntil *time.Time,
) model.Terms {
	t.Helper()
	row := model.Terms{
		ID: uuid.NewString(), Version: version, Title: title, Content: "", Status: status,
		EffectiveFrom: effectiveFrom, EffectiveUntil: effectiveUntil,
	}
	seedPublicLegalContentDocument(t, db, store, "terms", row.ID, title, body, &row.ContentDocumentID, &row)
	return row
}

func seedPublicLegalContentDocument(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	kind string,
	entityID string,
	title string,
	body string,
	documentIDTarget **string,
	root any,
) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: "policy", SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		documentID := created.Document.ID.String()
		*documentIDTarget = &documentID
		if err := tx.Create(root).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(
			created.Document.ID,
			created.Document.Revision,
			publicLegalRichTextDocument(body),
		)
		if err != nil {
			return err
		}
		_, err = store.ReplaceSnapshot(
			t.Context(),
			tx,
			replacement,
			func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
				return contentblock.DomainContext{SourceLocale: "en"}, nil
			},
		)
		if err != nil {
			return err
		}
		return tx.Table(kind+"_history").Where("id = ?", entityID).
			Update("source_locale", "en").Error
	}))
}

func publicLegalRichTextDocument(text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY, SourceLocale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
					Text: &contentv1.RichTextStyledText{Text: text},
				}}},
			}},
		}}}},
	}
}

func publicLegalDocumentText(document *contentv1.LocalizedRichTextDocument) string {
	if document == nil || document.LocaleOverlay == nil || len(document.LocaleOverlay.Blocks) != 1 {
		return ""
	}
	paragraph := document.LocaleOverlay.Blocks[0].GetParagraph()
	if paragraph == nil || len(paragraph.Content) != 1 {
		return ""
	}
	return paragraph.Content[0].GetText().GetText()
}

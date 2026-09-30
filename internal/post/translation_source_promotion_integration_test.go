//go:build integration

package post_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostLateProviderTranslationCannotOverwritePromotedSourceIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminIdentityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Post source promotion admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminIdentityID)
	memberID := testutil.PostIntegrationMemberID(adminIdentityID)
	store := testutil.NewPostContentBlockStore(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminIdentityID, "en")),
		store,
	)

	slug := "post-source-promotion-" + testutil.PostIntegrationUUID()
	created, err := service.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "English source title", Slug: &slug, Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	canonicalTitle := "Korean source title"
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO post_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, 'ko', ?, ?, ?)
	`, created.Msg.Id, canonicalTitle, now, now).Error)
	require.NoError(t, db.Table("post").Where("id = ?::uuid", created.Msg.Id).
		Update("source_locale", "ko").Error)

	job := &model.TranslationJob{
		EntityType: "post", EntityID: created.Msg.Id, SourceLocale: "en", TargetLocale: "ko",
		RequestedByMemberID: memberID,
	}
	staleTitle := "late machine translation"
	candidate := &translation.Candidate{
		Title:                     &staleTitle,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}
	plan := &translation.ExtractionPlan{Units: []translation.Unit{
		translation.NewEntityUnit("post", created.Msg.Id, "en", "title", "English source title"),
	}}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, map[string]translation.UnitResult{
		"entity:title": {UnitID: "entity:title", TranslatedText: staleTitle},
	}))

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return postdomain.ApplyTypedTranslationCandidateWithDB(
			ctx, tx, store, job, candidate,
			translation.EntryWrite{Title: &staleTitle, Now: time.Now().UTC()}, nil,
		)
	})
	require.ErrorIs(t, err, translation.ErrSourceNoLongerCurrent)

	var persisted string
	require.NoError(t, db.Table("post_translation").Select("title").
		Where("entity_id = ?::uuid AND locale = 'ko'", created.Msg.Id).Take(&persisted).Error)
	require.Equal(t, canonicalTitle, persisted)
}

//go:build integration

package integration

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkLateProviderTranslationCannotOverwritePromotedSourceIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work source promotion admin")
	ctx := workIntegrationAdminCtx(adminID)
	workService := newWorkIntegrationService(t, db, adminID, struct{}{})
	present := true
	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "English source title", Type: managev1.WorkType_WORK_TYPE_ARTICLE,
		Year: 2026, Month: 8, IsPresent: &present, Document: emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	_, err = uuid.Parse(created.Msg.Revision)
	require.NoError(t, err, "the candidate must reach source-role validation with a valid document revision")
	memberID := integrationMemberID(adminID)
	canonicalTitle := "Korean source title"
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO work_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, 'ko', ?, ?, ?)
	`, created.Msg.Id, canonicalTitle, now, now).Error)
	require.NoError(t, db.Table("work").Where("id = ?::uuid", created.Msg.Id).
		Update("source_locale", "ko").Error)

	store, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(integrationSpiceDB(t)))
	require.NoError(t, err)
	job := &model.TranslationJob{
		EntityType: "work", EntityID: created.Msg.Id, SourceLocale: "en", TargetLocale: "ko",
		RequestedByMemberID: memberID,
	}
	staleTitle := "late machine translation"
	candidate := &translation.Candidate{
		Title:                     &staleTitle,
		ContentDocumentRevision:   created.Msg.Revision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}
	plan := &translation.ExtractionPlan{Units: []translation.Unit{
		translation.NewEntityUnit("work", created.Msg.Id, "en", "title", "English source title"),
	}}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, map[string]translation.UnitResult{
		"entity:title": {UnitID: "entity:title", TranslatedText: staleTitle},
	}))

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return workdomain.ApplyTypedTranslationCandidateWithDB(
			ctx, tx, store, job, candidate,
			translation.EntryWrite{Title: &staleTitle, Now: time.Now().UTC()}, nil,
		)
	})
	require.ErrorIs(t, err, translation.ErrSourceNoLongerCurrent)

	var persisted string
	require.NoError(t, db.Table("work_translation").Select("title").
		Where("entity_id = ?::uuid AND locale = 'ko'", created.Msg.Id).Take(&persisted).Error)
	require.Equal(t, canonicalTitle, persisted)
}

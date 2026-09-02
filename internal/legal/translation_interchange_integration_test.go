//go:build integration

package legal_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLegalTranslationInterchangeCreateDeleteRecreateAndCASIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		create func(context.Context, *gorm.DB, *contentblock.Store, *auth.SpiceDBClient) (string, error)
	}{
		{
			name: "privacy",
			create: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				created, err := newPrivacyServiceForLegalIntegrationTest(
					db, "", "", spiceDB, legaldomain.WithPrivacyContentBlockStore(store),
				).CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
					Title: ptrString("Privacy XLIFF"), Document: legalPolicyDocumentFixture("en", "privacy source"),
				}))
				if err != nil {
					return "", err
				}
				return created.Msg.Id, nil
			},
		},
		{
			name: "terms",
			create: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				created, err := newTermsServiceForLegalIntegrationTest(
					db, "", "", spiceDB, legaldomain.WithTermsContentBlockStore(store),
				).CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{
					Title: ptrString("Terms XLIFF"), Document: legalPolicyDocumentFixture("en", "terms source"),
				}))
				if err != nil {
					return "", err
				}
				return created.Msg.Id, nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newLegalIntegrationDB(t)
			adminCtx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
			store := newLegalLifecycleContentBlockStore(t, spiceDB)
			entityID, err := testCase.create(adminCtx, db, store, spiceDB)
			require.NoError(t, err)
			domain, err := legaldomain.NewAuditedAIDocumentService(
				db, store, spiceDB, legalIntegrationDependencies(db, "").OG, noopDomainAuditAppender{},
			)
			require.NoError(t, err)
			port := translationadapter.NewLegalInterchange(domain)

			plan, source := loadLegalInterchangePlanIntegration(t, db, store, testCase.name, entityID, "ko")
			originalDocumentRevision := source.ContentDocumentRevision
			originalSourceLocale := loadLegalSourceLocaleIntegration(t, db, testCase.name, entityID)
			command := legalInterchangeReplaceCommandIntegration(testCase.name, entityID, plan, source)
			created := applyLegalInterchangeIntegration(t, adminCtx, db, store, spiceDB, port, command)
			require.True(t, created.Changed)
			require.NotEmpty(t, created.Revision)
			require.Len(t, created.AffectedUnitHandles, len(plan.Units))
			firstTargetRevision := created.Revision

			exported := loadLegalInterchangeIntegration(
				t, adminCtx, db, store, spiceDB, port, testCase.name, entityID, "ko", plan,
			)
			require.True(t, exported.Exists)
			require.Equal(t, firstTargetRevision, exported.Revision)
			require.Len(t, exported.Targets, len(plan.Units))
			assertLegalSourceIdentityUnchangedIntegration(
				t, db, store, testCase.name, entityID, originalDocumentRevision, originalSourceLocale,
			)

			current, err := domain.LoadAIDocument(adminCtx, testCase.name, entityID, "ko")
			require.NoError(t, err)
			require.NotNil(t, current.TargetRevision)
			sourceBlockID := source.ContentBlockDocument.GetBase().GetNodes()[0].GetBlock().GetId()
			_, err = domain.ExecuteAIDocumentMutation(
				adminCtx, testCase.name, entityID, "ko", legaldomain.AIDocumentExecutionApply,
				func(locked legaldomain.AIDocument) (legaldomain.AIDocumentMutation, error) {
					return legaldomain.AIDocumentMutation{
						EntityType: testCase.name, EntityID: entityID, Locale: "ko",
						ExpectedRevision: locked.Revision, ExpectedTargetRevision: locked.TargetRevision,
						ContributorMemberID: locked.ViewerMemberID,
						Content: &contentblock.Batch{
							DocumentID: locked.DocumentID, ExpectedRevision: uuid.MustParse(locked.Revision),
							ContributorMemberIDs: []uuid.UUID{uuid.MustParse(locked.ViewerMemberID)},
							LocaleGroups: []contentblock.LocaleMutationGroup{{
								Locale: "ko", Deletes: []uuid.UUID{uuid.MustParse(sourceBlockID)},
							}},
						},
					}, nil
				},
			)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

			_, err = domain.ExecuteAIDocumentMutation(
				adminCtx, testCase.name, entityID, "ko", legaldomain.AIDocumentExecutionApply,
				func(current legaldomain.AIDocument) (legaldomain.AIDocumentMutation, error) {
					return legaldomain.AIDocumentMutation{
						EntityType: testCase.name, EntityID: entityID, Locale: "ko",
						ExpectedRevision: current.Revision, Translation: legaldomain.AITranslationDelete,
						ExpectedTargetRevision: current.TargetRevision,
						ContributorMemberID:    current.ViewerMemberID,
					}, nil
				},
			)
			require.NoError(t, err)
			var targetCount int64
			require.NoError(t, db.Table(testCase.name+"_translation").
				Where("entity_id = ? AND locale = 'ko'", entityID).Count(&targetCount).Error)
			require.Zero(t, targetCount)
			assertLegalSourceIdentityUnchangedIntegration(
				t, db, store, testCase.name, entityID, originalDocumentRevision, originalSourceLocale,
			)

			plan, source = loadLegalInterchangePlanIntegration(t, db, store, testCase.name, entityID, "ko")
			command = legalInterchangeReplaceCommandIntegration(testCase.name, entityID, plan, source)
			recreated := applyLegalInterchangeIntegration(t, adminCtx, db, store, spiceDB, port, command)
			require.True(t, recreated.Changed)
			require.NotEqual(t, firstTargetRevision, recreated.Revision)
			assertLegalSourceIdentityUnchangedIntegration(
				t, db, store, testCase.name, entityID, originalDocumentRevision, originalSourceLocale,
			)

			jaPlan, jaSource := loadLegalInterchangePlanIntegration(
				t, db, store, testCase.name, entityID, "ja",
			)
			jaCommand := legalInterchangeReplaceCommandIntegration(
				testCase.name, entityID, jaPlan, jaSource,
			)
			jaCreated := applyLegalInterchangeIntegration(
				t, adminCtx, db, store, spiceDB, port, jaCommand,
			)
			require.True(t, jaCreated.Changed)
			koAfterJA := loadLegalInterchangeIntegration(
				t, adminCtx, db, store, spiceDB, port, testCase.name, entityID, "ko", plan,
			)
			require.Equal(t, recreated.Revision, koAfterJA.Revision)
			assertLegalSourceIdentityUnchangedIntegration(
				t, db, store, testCase.name, entityID, originalDocumentRevision, originalSourceLocale,
			)

			staleTitle := "stale title"
			staleCommand := application.TranslationInterchangeApply{
				EntityType: testCase.name, EntityID: entityID,
				SourceLocale: "en", TargetLocale: "ko",
				Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
				ExpectedRevision: &firstTargetRevision,
				Source:           source, Plan: plan,
				Targets: map[string]translation.UnitResult{
					"entity:title": {UnitID: "entity:title", TranslatedText: staleTitle},
				},
				UnitHandles: []string{"entity:title"},
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				if authErr := legaldomain.RequireEditableTranslationMutationWithDB(
					adminCtx, tx, spiceDB, testCase.name, entityID,
				); authErr != nil {
					return authErr
				}
				_, applyErr := port.ApplyTranslationInterchange(adminCtx, tx, store, staleCommand)
				return applyErr
			})
			require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
		})
	}
}

func loadLegalSourceLocaleIntegration(
	t *testing.T,
	db *gorm.DB,
	entityType string,
	entityID string,
) string {
	t.Helper()
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	require.NoError(t, db.Where(
		"id = ?", entityID,
	).Table(entityType+"_history").Select("source_locale").Take(&state).Error)
	return state.SourceLocale
}

func assertLegalSourceIdentityUnchangedIntegration(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	documentRevision string,
	sourceLocale string,
) {
	t.Helper()
	loaded, err := legaldomain.LoadTypedTranslationSourceDocument(
		t.Context(), db, store, entityType, entityID,
	)
	require.NoError(t, err)
	require.Equal(t, documentRevision, loaded.ContentDocumentRevision)
	require.Equal(t, sourceLocale, loaded.SourceLocale)
}

func loadLegalInterchangePlanIntegration(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	targetLocale string,
) (*translation.ExtractionPlan, *translation.SourceDocument) {
	t.Helper()
	source, err := legaldomain.LoadTypedTranslationSourceDocument(
		t.Context(), db, store, entityType, entityID,
	)
	require.NoError(t, err)
	plan, err := legaldomain.BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: entityType, EntityID: entityID, SourceLocale: source.SourceLocale, TargetLocale: targetLocale,
	}, source)
	require.NoError(t, err)
	return plan, source
}

func legalInterchangeReplaceCommandIntegration(
	entityType string,
	entityID string,
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
) application.TranslationInterchangeApply {
	targets := make(map[string]translation.UnitResult, len(plan.Units))
	handles := make([]string, 0, len(plan.Units))
	for _, unit := range plan.Units {
		value := "translated " + unit.UnitID
		targets[unit.UnitID] = translation.UnitResult{
			UnitID: unit.UnitID, TranslatedText: value,
		}
		handles = append(handles, unit.UnitID)
	}
	return application.TranslationInterchangeApply{
		EntityType: entityType, EntityID: entityID,
		SourceLocale: plan.SourceLocale, TargetLocale: plan.TargetLocale,
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: source, Plan: plan, Targets: targets, UnitHandles: handles,
	}
}

func applyLegalInterchangeIntegration(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	spiceDB *auth.SpiceDBClient,
	port *translationadapter.LegalInterchange,
	command application.TranslationInterchangeApply,
) application.TranslationInterchangeApplyResult {
	t.Helper()
	var result application.TranslationInterchangeApplyResult
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := legaldomain.RequireEditableTranslationMutationWithDB(
			ctx, tx, spiceDB, command.EntityType, command.EntityID,
		); err != nil {
			return err
		}
		var err error
		result, err = port.ApplyTranslationInterchange(ctx, tx, store, command)
		return err
	}))
	return result
}

func loadLegalInterchangeIntegration(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	spiceDB *auth.SpiceDBClient,
	port *translationadapter.LegalInterchange,
	entityType string,
	entityID string,
	locale string,
	plan *translation.ExtractionPlan,
) application.TranslationInterchangeTargetState {
	t.Helper()
	var state application.TranslationInterchangeTargetState
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := legaldomain.RequireTranslationInterchangeViewWithDB(
			ctx, tx, spiceDB, entityType, entityID,
		); err != nil {
			return err
		}
		var err error
		state, err = port.LoadTranslationInterchangeTarget(
			ctx, tx, store, entityType, entityID, locale, plan,
		)
		return err
	}))
	return state
}

package menu_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (f *menuFixture) joined(audit *audittest.Recorder) menu.Joined {
	return menu.Joined{Store: f.store, Audit: audit}
}

func TestTranslationSourceDocumentUsesTheStoredSourceValuesOnly(t *testing.T) {
	f := newMenuFixture(t)
	root := f.store.Seed(menu.Root{
		Name: "Main", SourceLocale: "en",
		ItemsJSON: []byte(`[{"id":"home","label":"루트 한글"},{"id":"about","label":"루트 소개"}]`),
	}, uuid.New(), menu.LocaleValues{Locale: "en", Labels: map[string]string{"home": ""}})

	document, err := menu.LoadTranslationSourceDocument(t.Context(), f.store, root.ID)
	require.NoError(t, err)
	var items []menu.Item
	require.NoError(t, json.Unmarshal(document.ContentJSON, &items))
	require.Empty(t, items[0].Label, "an explicit empty source value stays empty")
	require.Empty(t, items[1].Label, "a missing source value never falls back to the root")

	unseeded := f.store.Seed(menu.Root{Name: "Unseeded", SourceLocale: "en", ItemsJSON: []byte(`[]`)}, uuid.New())
	_, err = menu.LoadTranslationSourceDocument(t.Context(), f.store, unseeded.ID)
	requireCode(t, err, connect.CodeFailedPrecondition, "Menu source locale values are not initialized")
	_, err = menu.LoadTranslationSourceDocument(t.Context(), f.store, uuid.NewString())
	require.ErrorIs(t, err, menu.ErrMenuNotFound)
}

func TestTranslationBoundariesLockTheMenuAndMaskAnInactivePrincipal(t *testing.T) {
	f := newMenuFixture(t)
	created, _ := f.createMenu(t, "Main", customItem("home", "/"))
	fresh := func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(f.ctx, nil, f.fence, f.checker, can)
	}

	require.NoError(t, menu.RequireViewAndLock(f.ctx, f.store, fresh, created.ID))
	require.NoError(t, menu.RequireEditAndLock(f.ctx, f.store, fresh, created.ID))
	checks := f.checker.Checks()
	require.Equal(t, authztest.Of(f.identityID, must(policyv1.Menu.View(created.ID)), nil), checks[len(checks)-2])
	require.Equal(t, authztest.Of(f.identityID, must(policyv1.Menu.Edit(created.ID)), nil), checks[len(checks)-1])

	missing := uuid.NewString()
	requireCode(t, menu.RequireEditAndLock(f.ctx, f.store, fresh, missing), connect.CodeNotFound, "menu not found: "+missing)

	f.checker.Deny(policyv1.Menu.Edit(created.ID))
	requireCode(t, menu.RequireEditAndLock(f.ctx, f.store, fresh, created.ID), connect.CodePermissionDenied, "no permission to edit this menu")

	f.fence.Deactivate(f.identityID)
	requireCode(t, menu.RequireViewAndLock(f.ctx, f.store, fresh, created.ID), connect.CodeNotFound, "menu not found: "+created.ID)
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func providerCandidate(t *testing.T, plan *translation.ExtractionPlan, results map[string]translation.UnitResult) *translation.Candidate {
	t.Helper()
	candidate := &translation.Candidate{}
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))
	return candidate
}

func interchange(menuID string, plan *translation.ExtractionPlan, mode menu.InterchangeMode, expected *string, targets map[string]translation.UnitResult) menu.TranslationInterchangeMutation {
	handles := make([]string, 0, len(targets))
	for handle := range targets {
		handles = append(handles, handle)
	}
	return menu.TranslationInterchangeMutation{
		MenuID: menuID, SourceLocale: translation.DefaultLocale, TargetLocale: plan.TargetLocale,
		Mode: mode, ExpectedRevision: expected, Plan: plan, Targets: targets, UnitHandles: handles, Now: time.Now().UTC(),
	}
}

func TestTranslationInterchangePersistsSparseTargetsWithCASAndAudit(t *testing.T) {
	f := newMenuFixture(t)
	created, _ := f.createMenu(t, "Menu XLIFF", customItem("home", "/"), customItem("about", "/about"))
	source, err := menu.LoadTranslationSourceDocument(f.ctx, f.store, created.ID)
	require.NoError(t, err)
	plan, err := menu.BuildTranslationExtractionPlan(created.ID, translation.DefaultLocale, "ko", source)
	require.NoError(t, err)
	audit := &audittest.Recorder{}
	emptyHome := map[string]translation.UnitResult{"item:home:label": {UnitID: "item:home:label", TranslatedText: ""}}

	absent, err := menu.LoadTranslationInterchangeTarget(f.ctx, f.store, created.ID, "ko", plan)
	require.NoError(t, err)
	require.False(t, absent.Exists)
	require.Empty(t, absent.Targets, "an absent locale never materializes source fallback")

	result, err := menu.ApplyTranslationInterchange(f.ctx, f.joined(audit), interchange(created.ID, plan, menu.InterchangePatch, nil, emptyHome))
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, map[string]string{"about": "about", "home": ""}, f.labels(t, created.ID, "ko"))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, audit.Records()[0].ItemOperation)
	loaded, err := menu.LoadTranslationInterchangeTarget(f.ctx, f.store, created.ID, "ko", plan)
	require.NoError(t, err)
	require.Equal(t, result.Revision, loaded.Revision)

	createdRevision := result.Revision
	result, err = menu.ApplyTranslationInterchange(f.ctx, f.joined(audit), interchange(created.ID, plan, menu.InterchangeReplace, &createdRevision, emptyHome))
	require.NoError(t, err)
	require.Equal(t, []string{"item:about:label"}, result.AffectedUnitHandles)
	require.Equal(t, map[string]string{"home": ""}, f.labels(t, created.ID, "ko"))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, audit.Records()[1].ItemOperation)

	_, err = menu.ApplyTranslationInterchange(f.ctx, f.joined(audit), interchange(created.ID, plan, menu.InterchangePatch, &createdRevision,
		map[string]translation.UnitResult{"item:home:label": {UnitID: "item:home:label", TranslatedText: "홈"}}))
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Len(t, audit.Records(), 2)

	sourcePlan := *plan
	sourcePlan.TargetLocale = translation.DefaultLocale
	_, err = menu.LoadTranslationInterchangeTarget(f.ctx, f.store, created.ID, translation.DefaultLocale, &sourcePlan)
	requireCode(t, err, connect.CodeInvalidArgument, "")
	_, err = menu.ApplyTranslationInterchange(f.ctx, menu.Joined{Store: f.store}, interchange(created.ID, plan, menu.InterchangePatch, nil, emptyHome))
	requireCode(t, err, connect.CodeUnavailable, "Menu translation interchange unavailable")
	_, err = menu.ApplyTranslationInterchange(f.ctx, f.joined(audit), interchange(created.ID, plan, 0, nil, emptyHome))
	requireCode(t, err, connect.CodeInvalidArgument, "mode: PATCH or REPLACE is required")
}

func TestTranslationInterchangeAuditFailureFailsTheJoinedTransaction(t *testing.T) {
	f := newMenuFixture(t)
	created, _ := f.createMenu(t, "Menu XLIFF", customItem("home", "/"))
	source, err := menu.LoadTranslationSourceDocument(f.ctx, f.store, created.ID)
	require.NoError(t, err)
	plan, err := menu.BuildTranslationExtractionPlan(created.ID, translation.DefaultLocale, "fr", source)
	require.NoError(t, err)
	auditDown := errors.New("audit unavailable")

	err = f.units.Transaction(f.ctx, false, func(tx mutation.Tx[menu.Store]) error {
		_, err := menu.ApplyTranslationInterchange(f.ctx, menu.Joined{Store: tx.Store(), Audit: &audittest.Recorder{Err: auditDown}},
			interchange(created.ID, plan, menu.InterchangePatch, nil, map[string]translation.UnitResult{"item:home:label": {UnitID: "item:home:label", TranslatedText: "Accueil"}}))
		return err
	})

	require.ErrorIs(t, err, auditDown)
	require.Nil(t, f.labels(t, created.ID, "fr"))
}

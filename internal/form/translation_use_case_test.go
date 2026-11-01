package form_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	providerSourceSchema = `{"id":"schema","steps":[{"id":"step-1","title":"Contact","description":"How can we help","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]}]}`
	frenchSourceSchema   = `{"id":"french-schema","steps":[{"id":"step-fr","title":"Français","fields":[]}]}`
)

// providerCandidate is a provider response to a job requested over the
// English source.
func providerCandidate(t *testing.T, formID, targetLocale string) (form.TranslationJob, *translation.Candidate) {
	t.Helper()
	requestSource := &translation.SourceDocument{Title: "Contact form", ContentJSON: []byte(providerSourceSchema)}
	plan, err := form.BuildTranslationExtractionPlan(formID, "en", targetLocale, requestSource)
	require.NoError(t, err)
	results := map[string]translation.UnitResult{
		"entity:title":            {UnitID: "entity:title", TranslatedText: "요청 시점 번역"},
		"step:step-1:title":       {UnitID: "step:step-1:title", TranslatedText: "문의"},
		"step:step-1:description": {UnitID: "step:step-1:description", TranslatedText: "도움"},
		"field:field-email:label": {UnitID: "field:field-email:label", TranslatedText: "이메일"},
	}
	candidate, err := form.ApplyTranslationCandidate(requestSource, results)
	require.NoError(t, err)
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))
	return form.TranslationJob{EntityType: "form", EntityID: formID, TargetLocale: targetLocale, RequestedByMemberID: uuid.NewString()}, candidate
}

func seedProviderForm(t *testing.T, store *formtest.Store) form.Form {
	t.Helper()
	stored := store.PutForm(form.Form{SourceLocale: "en"})
	store.PutLocale(stored.ID, "en", formtest.StoredLocale{Title: new("Contact form"), ContentJSON: []byte(providerSourceSchema)})
	return stored
}

func TestProviderTranslationWritesTheTargetOverlayForTheRequester(t *testing.T) {
	store, _ := formtest.New()
	stored := seedProviderForm(t, store)
	audit := &audittest.Recorder{}
	job, candidate := providerCandidate(t, stored.ID, "ko")
	before := store.DocumentRevisionOf(stored.ContentDocumentID)

	require.NoError(t, form.ApplyProviderTranslationCandidate(t.Context(), form.Joined{Store: store, Audit: audit}, job, candidate,
		translation.EntryWrite{Now: time.Now()}))

	target, ok := store.StoredLocale(stored.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "요청 시점 번역", *target.Title)
	require.JSONEq(t, `{"id":"schema","steps":[{"id":"step-1","title":"문의","description":"도움","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`, string(target.ContentJSON))
	require.Equal(t, before, store.DocumentRevisionOf(stored.ContentDocumentID), "a target write keeps the document revision")
	records := audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, job.RequestedByMemberID, records[0].MemberID)
	require.Equal(t, "ko", records[0].Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, records[0].ItemOperation)
}

// A source locale switch after the request projects the response onto the
// current source topology; a target that has become the source advances
// the document.
func TestProviderTranslationFollowsTheCurrentSourceLocale(t *testing.T) {
	store, _ := formtest.New()
	stored := seedProviderForm(t, store)
	store.PutLocale(stored.ID, "fr", formtest.StoredLocale{Title: new(""), ContentJSON: []byte(frenchSourceSchema)})
	switched := stored
	switched.SourceLocale = "fr"
	store.PutForm(switched)
	source, err := form.LoadTranslationSourceDocument(t.Context(), store, stored.ID)
	require.NoError(t, err)
	require.Empty(t, source.Title)
	require.JSONEq(t, frenchSourceSchema, string(source.ContentJSON))

	audit := &audittest.Recorder{}
	job, candidate := providerCandidate(t, stored.ID, "ko")
	require.NoError(t, form.ApplyProviderTranslationCandidate(t.Context(), form.Joined{Store: store, Audit: audit}, job, candidate,
		translation.EntryWrite{Now: time.Now()}))
	target, _ := store.StoredLocale(stored.ID, "ko")
	require.JSONEq(t, `{"id":"french-schema","steps":[{"id":"step-fr","fields":[]}]}`, string(target.ContentJSON),
		"the late response keeps only the units the current source has")
	require.Equal(t, "요청 시점 번역", *target.Title, "an explicit empty source title keeps its handle")

	before := store.DocumentRevisionOf(stored.ContentDocumentID)
	job, candidate = providerCandidate(t, stored.ID, "fr")
	require.NoError(t, form.ApplyProviderTranslationCandidate(t.Context(), form.Joined{Store: store, Audit: audit}, job, candidate,
		translation.EntryWrite{Now: time.Now()}))
	require.NotEqual(t, before, store.DocumentRevisionOf(stored.ContentDocumentID), "a write of the current source advances the document")
	require.Len(t, audit.Records(), 2)
}

func TestProviderTranslationDoesNotReviveAnAbsentSourceTitle(t *testing.T) {
	store, _ := formtest.New()
	stored := store.PutForm(form.Form{SourceLocale: "en"})
	store.PutLocale(stored.ID, "en", formtest.StoredLocale{ContentJSON: []byte(providerSourceSchema)})
	job, candidate := providerCandidate(t, stored.ID, "ko")

	require.NoError(t, form.ApplyProviderTranslationCandidate(t.Context(), form.Joined{Store: store, Audit: &audittest.Recorder{}}, job, candidate,
		translation.EntryWrite{Now: time.Now()}))

	target, _ := store.StoredLocale(stored.ID, "ko")
	require.Nil(t, target.Title)
}

func TestPrepareSourceLocaleSwitchCreatesAnEmptyOverlayOnce(t *testing.T) {
	store, _ := formtest.New()
	stored := seedProviderForm(t, store)
	now := time.Now()

	require.NoError(t, form.PrepareSourceLocaleSwitch(t.Context(), store, stored.ID, "en", "ja", now))
	prepared, ok := store.StoredLocale(stored.ID, "ja")
	require.True(t, ok)
	require.Nil(t, prepared.Title)
	require.Nil(t, prepared.ContentText)
	require.JSONEq(t, `{"id":"schema","steps":[{"id":"step-1","fields":[{"id":"field-email","key":"email","type":"email"}]}]}`, string(prepared.ContentJSON))
	require.NoError(t, form.PrepareSourceLocaleSwitch(t.Context(), store, stored.ID, "en", "ja", now), "an existing overlay is kept")

	err := form.PrepareSourceLocaleSwitch(t.Context(), store, stored.ID, "ko", "ja", now)
	requireCode(t, err, connect.CodeFailedPrecondition, "Form source locale changed")
	err = form.PrepareSourceLocaleSwitch(t.Context(), store, stored.ID, "en", "en", now)
	requireCode(t, err, connect.CodeInvalidArgument, "")
}

func TestTranslationInterchangeStoresTheSparseTargetAndChecksItsRevision(t *testing.T) {
	store, _ := formtest.New()
	stored := seedProviderForm(t, store)
	editor := newPrincipal()
	ctx := editor.context(t)
	audit := &audittest.Recorder{}
	source, err := form.LoadTranslationSourceDocument(ctx, store, stored.ID)
	require.NoError(t, err)
	plan, err := form.BuildTranslationExtractionPlan(stored.ID, "en", "ko", source)
	require.NoError(t, err)
	targets := map[string]translation.UnitResult{
		"entity:title":            {UnitID: "entity:title", TranslatedText: ""},
		"step:step-1:title":       {UnitID: "step:step-1:title", TranslatedText: "문의"},
		"field:field-email:label": {UnitID: "field:field-email:label", TranslatedText: ""},
	}
	mutation := form.TranslationInterchangeMutation{
		FormID: stored.ID, SourceLocale: "en", TargetLocale: "ko", Mode: form.InterchangePatch,
		Source: source, Plan: plan, Targets: targets,
		UnitHandles: []string{"entity:title", "step:step-1:title", "field:field-email:label"}, Now: time.Now(),
	}

	absent, err := form.LoadTranslationInterchangeTarget(ctx, store, stored.ID, "ko", plan)
	require.NoError(t, err)
	require.False(t, absent.Exists)
	result, err := form.ApplyTranslationInterchange(ctx, form.Joined{Store: store, Audit: audit}, mutation)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEmpty(t, result.Revision)
	target, _ := store.StoredLocale(stored.ID, "ko")
	require.Empty(t, *target.Title)
	require.JSONEq(t, `{"id":"schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"","type":"email"}]}]}`, string(target.ContentJSON))
	require.Len(t, audit.Records(), 1)
	require.Equal(t, editor.memberID, audit.Records()[0].MemberID)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, audit.Records()[0].ItemOperation)

	loaded, err := form.LoadTranslationInterchangeTarget(ctx, store, stored.ID, "ko", plan)
	require.NoError(t, err)
	require.True(t, loaded.Exists)
	require.Equal(t, result.Revision, loaded.Revision)
	require.Equal(t, "문의", loaded.Targets["step:step-1:title"].TranslatedText)

	unchanged := mutation
	unchanged.ExpectedRevision = &result.Revision
	again, err := form.ApplyTranslationInterchange(ctx, form.Joined{Store: store, Audit: audit}, unchanged)
	require.NoError(t, err)
	require.False(t, again.Changed)
	require.Equal(t, result.Revision, again.Revision)

	staleAbsent := mutation
	staleAbsent.ExpectedRevision = new("")
	_, err = form.ApplyTranslationInterchange(ctx, form.Joined{Store: store, Audit: audit}, staleAbsent)
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Len(t, audit.Records(), 1)
}

func TestTranslationInterchangeFailsWithItsAudit(t *testing.T) {
	store, _ := formtest.New()
	stored := seedProviderForm(t, store)
	ctx := newPrincipal().context(t)
	source, err := form.LoadTranslationSourceDocument(ctx, store, stored.ID)
	require.NoError(t, err)
	plan, err := form.BuildTranslationExtractionPlan(stored.ID, "en", "fr", source)
	require.NoError(t, err)
	audit := &audittest.Recorder{Err: errors.New("audit unavailable")}

	_, err = form.ApplyTranslationInterchange(ctx, form.Joined{Store: store, Audit: audit}, form.TranslationInterchangeMutation{
		FormID: stored.ID, SourceLocale: "en", TargetLocale: "fr", Mode: form.InterchangePatch, Source: source, Plan: plan,
		Targets:     map[string]translation.UnitResult{"entity:title": {UnitID: "entity:title", TranslatedText: "Contact"}},
		UnitHandles: []string{"entity:title"}, Now: time.Now(),
	})

	require.ErrorIs(t, err, audit.Err, "the joined transaction rolls the write back")
}

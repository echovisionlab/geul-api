//go:build integration

package legal_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type legalTranslationFixture struct {
	db       *gorm.DB
	store    *contentblock.Store
	kind     string
	memberID string
	create   func(t *testing.T) (string, *translation.SourceDocument)
}

// TestLegalApplyTypedTranslationCandidateIntegration pins every branch of the
// translation-job write path for both policy kinds: the rows it writes, the
// Domain Audit it appends, and the exact rejection for each guard.
func TestLegalApplyTypedTranslationCandidateIntegration(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"terms", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newLegalTranslationFixture(t, kind)
			t.Run("rejections", func(t *testing.T) { testLegalTranslationRejections(t, fixture) })
			t.Run("target lifecycle", func(t *testing.T) { testLegalTranslationTargetLifecycle(t, fixture) })
			t.Run("provider target", func(t *testing.T) { testLegalTranslationProviderTarget(t, fixture) })
			t.Run("provider source", func(t *testing.T) { testLegalTranslationProviderSource(t, fixture) })
		})
	}
}

func newLegalTranslationFixture(t *testing.T, kind string) legalTranslationFixture {
	t.Helper()
	db := newLegalLocalKratosIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Legal translation admin")
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	store := newLegalLifecycleContentBlockStore(t, spiceDB)
	writer := apitelemetry.NewDurableWriter(db)
	fixture := legalTranslationFixture{db: db, store: store, kind: kind, memberID: memberID}
	fixture.create = func(t *testing.T) (string, *translation.SourceDocument) {
		t.Helper()
		var id string
		if kind == "terms" {
			created, err := legaldomain.NewAuditedTermsService(
				db, "https://example.com", writer, spiceDB, legalIntegrationDependencies(db, ""),
				legaldomain.WithTermsContentBlockStore(store),
			).CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{
				Title: ptrString("Source terms"), Document: legalPolicyDocumentFixture("en", "Source terms text"),
			}))
			require.NoError(t, err)
			id = created.Msg.Id
		} else {
			created, err := legaldomain.NewAuditedPrivacyService(
				db, "https://example.com", writer, spiceDB, legalIntegrationDependencies(db, ""),
				legaldomain.WithPrivacyContentBlockStore(store),
			).CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
				Title: ptrString("Source privacy"), Document: legalPolicyDocumentFixture("en", "Source privacy text"),
			}))
			require.NoError(t, err)
			id = created.Msg.Id
		}
		source, err := legaldomain.LoadTypedTranslationSourceDocument(t.Context(), db, store, kind, id)
		require.NoError(t, err)
		return id, source
	}
	return fixture
}

func (f legalTranslationFixture) apply(job *model.TranslationJob, candidate *translation.Candidate) error {
	return f.db.Transaction(func(tx *gorm.DB) error {
		return legaldomain.ApplyTypedTranslationCandidateWithDB(
			context.Background(), tx, f.store, job, candidate, apitelemetry.NewDurableWriter(f.db),
		)
	})
}

func (f legalTranslationFixture) job(id, targetLocale string) *model.TranslationJob {
	return &model.TranslationJob{
		EntityType: f.kind, EntityID: id, SourceLocale: "en", TargetLocale: targetLocale,
		RequestedByMemberID: f.memberID,
	}
}

func legalTranslationCandidate(
	source *translation.SourceDocument,
	locale string,
	title string,
	text string,
) *translation.Candidate {
	overlay := proto.Clone(source.ContentBlockDocument.GetLocaleOverlay()).(*contentv1.RichTextLocaleOverlay)
	overlay.Locale = locale
	overlay.Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
		Text: &contentv1.RichTextStyledText{Text: text},
	}}}
	return &translation.Candidate{
		Title: &title, ContentDocumentRevision: source.ContentDocumentRevision, ContentBlockLocaleOverlay: overlay,
	}
}

type legalTranslationState struct {
	SourceTitle      string
	SourceText       string
	DocumentRevision string
	Targets          map[string]legalTranslationTarget
}

type legalTranslationTarget struct {
	Title       string
	ContentText string
	BlockTexts  int64
}

func (f legalTranslationFixture) state(t *testing.T, id string) legalTranslationState {
	t.Helper()
	var root struct {
		Title             string `gorm:"column:title"`
		ContentText       string `gorm:"column:content_text"`
		ContentDocumentID string `gorm:"column:content_document_id"`
		Revision          string `gorm:"column:revision"`
	}
	require.NoError(t, f.db.Raw(
		`SELECT history.title, COALESCE(history.content_text, '') AS content_text,
			history.content_document_id::text AS content_document_id, document.revision::text AS revision
		FROM `+f.kind+`_history history JOIN content_document document ON document.id = history.content_document_id
		WHERE history.id = ?`, id,
	).Scan(&root).Error)
	var rows []struct {
		Locale      string `gorm:"column:locale"`
		Title       string `gorm:"column:title"`
		ContentText string `gorm:"column:content_text"`
	}
	require.NoError(t, f.db.Raw(
		`SELECT locale, COALESCE(title, '') AS title, COALESCE(content_text, '') AS content_text
		FROM `+f.kind+`_translation WHERE entity_id = ?`, id,
	).Scan(&rows).Error)
	targets := make(map[string]legalTranslationTarget, len(rows))
	for _, row := range rows {
		var blocks int64
		require.NoError(t, f.db.Raw(
			`SELECT COUNT(*) FROM content_block_locale locale_value
			JOIN content_block block ON block.id = locale_value.block_id
			WHERE block.document_id = ?::uuid AND locale_value.locale = ?`, root.ContentDocumentID, row.Locale,
		).Scan(&blocks).Error)
		targets[row.Locale] = legalTranslationTarget{Title: row.Title, ContentText: row.ContentText, BlockTexts: blocks}
	}
	return legalTranslationState{
		SourceTitle: root.Title, SourceText: root.ContentText, DocumentRevision: root.Revision, Targets: targets,
	}
}

func testLegalTranslationRejections(t *testing.T, f legalTranslationFixture) {
	id, source := f.create(t)
	before := f.state(t, id)
	missingID := uuid.NewString()
	valid := func() *translation.Candidate {
		return legalTranslationCandidate(source, "ko", "번역 제목", "번역 본문")
	}
	for _, testCase := range []struct {
		name      string
		job       *model.TranslationJob
		candidate *translation.Candidate
		code      connect.Code
		message   string
		sentinel  error
	}{
		{
			name: "missing candidate", job: f.job(id, "ko"), candidate: nil,
			code: connect.CodeInternal, message: "legal typed translation candidate and Content Block transaction are required",
		},
		{
			name: "unsupported entity type", job: &model.TranslationJob{
				EntityType: "post", EntityID: id, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: f.memberID,
			}, candidate: valid(),
			code: connect.CodeInvalidArgument, message: "entity_type: legal typed translation requires terms or privacy",
		},
		{
			name: "non-canonical source locale", job: &model.TranslationJob{
				EntityType: f.kind, EntityID: id, SourceLocale: "english", TargetLocale: "ko", RequestedByMemberID: f.memberID,
			}, candidate: valid(),
			code: connect.CodeInvalidArgument, message: "locale: must be an exact canonical locale",
		},
		{
			name: "non-canonical target locale", job: f.job(id, "korean"), candidate: valid(),
			code: connect.CodeInvalidArgument, message: "locale: must be an exact canonical locale",
		},
		{
			name: "overlay locale differs from target", job: f.job(id, "ja"), candidate: valid(),
			code: connect.CodeInvalidArgument, message: "document.locale_overlay.locale: must match the translation target locale",
		},
		{
			name: "blank title", job: f.job(id, "ko"), candidate: legalTranslationCandidate(source, "ko", "  ", "번역 본문"),
			code: connect.CodeFailedPrecondition, message: "legal target locale title is required",
		},
		{
			name: "missing policy", job: f.job(missingID, "ko"), candidate: valid(),
			code: connect.CodeNotFound, message: f.kind + " not found: " + missingID,
		},
		{
			name: "unparseable source revision", job: f.job(id, "ko"), candidate: func() *translation.Candidate {
				candidate := valid()
				candidate.ContentDocumentRevision = "not-a-revision"
				return candidate
			}(),
			sentinel: translation.ErrSourceNoLongerCurrent,
		},
		{
			name: "stale source revision", job: f.job(id, "ko"), candidate: func() *translation.Candidate {
				candidate := valid()
				candidate.ContentDocumentRevision = uuid.NewString()
				return candidate
			}(),
			sentinel: translation.ErrSourceNoLongerCurrent,
		},
		{
			name: "target is the source locale", job: f.job(id, "en"),
			candidate: legalTranslationCandidate(source, "en", "Source rewrite", "Source rewrite text"),
			code:      connect.CodeInvalidArgument, message: "locale: legal target locale must differ from the source locale",
		},
		{
			name: "requester Member is missing", job: &model.TranslationJob{
				EntityType: f.kind, EntityID: id, SourceLocale: "en", TargetLocale: "ko",
			}, candidate: valid(),
			code: connect.CodeInternal, message: "legal translation audit requires requester Member",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := f.apply(testCase.job, testCase.candidate)
			if testCase.sentinel != nil {
				require.ErrorIs(t, err, testCase.sentinel)
			} else {
				requireLegalParityError(t, err, testCase.code, testCase.message)
			}
			require.Equal(t, before, f.state(t, id), "a rejected translation writes nothing")
			require.Empty(t, legalParityAuditRows(t, f.db, id)[1:], "only the create audit exists")
		})
	}
}

func testLegalTranslationTargetLifecycle(t *testing.T, f legalTranslationFixture) {
	id, source := f.create(t)
	before := f.state(t, id)
	require.Empty(t, before.Targets)

	require.NoError(t, f.apply(f.job(id, "ko"), legalTranslationCandidate(source, "ko", " 번역 제목 ", "번역 본문")))
	created := f.state(t, id)
	require.Equal(t, before.DocumentRevision, created.DocumentRevision, "a target write keeps the source revision")
	require.Equal(t, before.SourceTitle, created.SourceTitle)
	require.Equal(t, map[string]legalTranslationTarget{
		"ko": {Title: "번역 제목", ContentText: "번역 본문", BlockTexts: 1},
	}, created.Targets)
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{{Locale: "ko", Operation: "created"}})

	require.NoError(t, f.apply(f.job(id, "ko"), legalTranslationCandidate(source, "ko", "번역 제목", "번역 본문")))
	require.Equal(t, created, f.state(t, id))
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{{Locale: "ko", Operation: "created"}})

	// A candidate translated from another source revision is discarded.
	stale := legalTranslationCandidate(source, "ko", "갱신 제목", "갱신 본문")
	stale.ContentDocumentRevision = uuid.NewString()
	require.ErrorIs(t, f.apply(f.job(id, "ko"), stale), translation.ErrSourceNoLongerCurrent)
	require.Equal(t, created, f.state(t, id))
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{{Locale: "ko", Operation: "created"}})

	require.NoError(t, f.apply(f.job(id, "ko"), legalTranslationCandidate(source, "ko", "갱신 제목", "갱신 본문")))
	require.Equal(t, map[string]legalTranslationTarget{
		"ko": {Title: "갱신 제목", ContentText: "갱신 본문", BlockTexts: 1},
	}, f.state(t, id).Targets)
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{
		{Locale: "ko", Operation: "created"}, {Locale: "ko", Operation: "updated"},
	})

	// A whole replacement deletes every current target Block it omits.
	emptyTitle := "빈 번역"
	require.NoError(t, f.apply(f.job(id, "ko"), &translation.Candidate{
		Title: &emptyTitle, ContentDocumentRevision: source.ContentDocumentRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}))
	replaced := f.state(t, id)
	require.Equal(t, before.DocumentRevision, replaced.DocumentRevision)
	// The derived target text falls back to the source text once the target
	// has no Block values of its own.
	require.Equal(t, legalTranslationTarget{Title: "빈 번역", ContentText: before.SourceText, BlockTexts: 0}, replaced.Targets["ko"])
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{
		{Locale: "ko", Operation: "created"}, {Locale: "ko", Operation: "updated"}, {Locale: "ko", Operation: "updated"},
	})
}

func testLegalTranslationProviderTarget(t *testing.T, f legalTranslationFixture) {
	id, source := f.create(t)
	before := f.state(t, id)
	job := f.job(id, "ja")
	plan, err := legaldomain.BuildTranslationExtractionPlan(job, source)
	require.NoError(t, err)
	results := make(map[string]translation.UnitResult, len(plan.Units))
	for _, unit := range plan.Units {
		results[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID, TranslatedText: "provider " + unit.UnitID}
	}
	candidate, err := legaldomain.BuildTranslationCandidate(plan, source, results)
	require.NoError(t, err)
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))

	require.NoError(t, f.apply(job, candidate))
	after := f.state(t, id)
	require.Equal(t, before.DocumentRevision, after.DocumentRevision)
	blockUnit := plan.Units[len(plan.Units)-1].UnitID
	require.Equal(t, map[string]legalTranslationTarget{
		"ja": {Title: "provider entity:title", ContentText: "provider " + blockUnit, BlockTexts: 1},
	}, after.Targets)
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{{Locale: "ja", Operation: "created"}})
}

func testLegalTranslationProviderSource(t *testing.T, f legalTranslationFixture) {
	id, source := f.create(t)
	before := f.state(t, id)
	planJob := f.job(id, "ko")
	plan, err := legaldomain.BuildTranslationExtractionPlan(planJob, source)
	require.NoError(t, err)
	results := make(map[string]translation.UnitResult, len(plan.Units))
	for _, unit := range plan.Units {
		results[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID, TranslatedText: "late " + unit.UnitID}
	}
	candidate := legalTranslationCandidate(source, "en", "Late provider source title", "ignored overlay text")
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))

	nonCanonical := f.job(id, "en")
	nonCanonical.RequestedByMemberID = strings.ToUpper(f.memberID)
	err = f.apply(nonCanonical, candidate)
	requireLegalParityError(t, err, connect.CodeInternal, "legal translation audit requires canonical requester Member")
	require.Equal(t, before, f.state(t, id))

	// A provider result requested for another source whose target is now
	// the source locale is discarded instead of rewriting the source.
	late := f.job(id, "en")
	late.SourceLocale = "ko"
	require.ErrorIs(t, f.apply(late, candidate), translation.ErrSourceNoLongerCurrent)
	require.Equal(t, before, f.state(t, id))
	require.Empty(t, legalParityAuditRows(t, f.db, id)[1:])

	// A provider result whose job names the source locale on both sides
	// rewrites the source title and the requested source units, as Post's
	// same-locale provider operation does.
	require.NoError(t, f.apply(f.job(id, "en"), candidate))
	after := f.state(t, id)
	require.NotEqual(t, before.DocumentRevision, after.DocumentRevision)
	blockUnit := plan.Units[len(plan.Units)-1].UnitID
	require.Equal(t, legalTranslationState{
		SourceTitle: "Late provider source title", SourceText: "late " + blockUnit,
		DocumentRevision: after.DocumentRevision, Targets: map[string]legalTranslationTarget{},
	}, after)
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{{Locale: "en", Operation: "updated"}})

	require.NoError(t, f.apply(f.job(id, "en"), candidate))
	require.Equal(t, after, f.state(t, id), "an unchanged provider source result is a no-op")
	requireLegalTranslationAudits(t, f, id, []legalTranslationAudit{{Locale: "en", Operation: "updated"}})
}

type legalTranslationAudit struct {
	Locale    string
	Operation string
}

// requireLegalTranslationAudits checks the audits appended after the policy
// was created.
func requireLegalTranslationAudits(t *testing.T, f legalTranslationFixture, id string, want []legalTranslationAudit) {
	t.Helper()
	rows := legalParityAuditRows(t, f.db, id)
	require.NotEmpty(t, rows)
	require.Equal(t, "legal_policy.created", rows[0].Action)
	createdVersion := legalParityAuditAttributes(t, rows[0])["version_number"]
	wantAttributes := make([]map[string]any, 0, len(want))
	for _, audit := range want {
		wantAttributes = append(wantAttributes, map[string]any{
			"policy_type": f.kind, "changed_fields": []any{"locale_content"},
			"locale": audit.Locale, "item_operation": audit.Operation,
		})
	}
	got := make([]map[string]any, 0, len(rows)-1)
	for _, row := range rows[1:] {
		require.Equal(t, "legal_policy.updated", row.Action)
		require.Equal(t, "member", row.ActorKind)
		require.Equal(t, f.memberID, row.ActorMemberID)
		require.Equal(t, "legal_policy", row.TargetType)
		attributes := legalParityAuditAttributes(t, row)
		require.Equal(t, createdVersion, attributes["version_number"], "a locale audit carries the policy version")
		delete(attributes, "version_number")
		got = append(got, attributes)
	}
	require.Equal(t, wantAttributes, got)
}

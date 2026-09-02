package translationadapter

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

func TestProjectEmailRichTextInterchangeTargetPreservesAbsentAndExplicitEmpty(t *testing.T) {
	blockID := uuid.NewString()
	base := blockInterchangeBase(paragraphBase(blockID))
	source := emailInterchangeDocument("en", base, paragraphLocale(blockID, "source"))
	plan := emailInterchangePlan(t, source, "email_template", uuid.NewString(), "ko")

	absent, err := projectEmailRichTextInterchangeTarget(
		plan, false, uuid.NewString(), nil, emailInterchangeDocument("ko", base),
	)
	if err != nil {
		t.Fatalf("project absent Email target: %v", err)
	}
	if absent.state.Exists || absent.state.Revision != "" || len(absent.state.Targets) != 0 {
		t.Fatalf("absent Email target = %+v", absent.state)
	}

	empty := ""
	revision := uuid.NewString()
	present, err := projectEmailRichTextInterchangeTarget(
		plan, true, revision, &empty, emailInterchangeDocument("ko", base, paragraphLocale(blockID, "")),
	)
	if err != nil {
		t.Fatalf("project explicit-empty Email target: %v", err)
	}
	for _, handle := range []string{"entity:title", "block:" + blockID + ":typed:paragraph/content"} {
		value, ok := present.state.Targets[handle]
		if !ok || value.TranslatedText != "" {
			t.Fatalf("explicit-empty %s = (%+v, %v)", handle, value, ok)
		}
	}
	if present.state.Revision != revision {
		t.Fatalf("present revision = %q, want %q", present.state.Revision, revision)
	}
}

func TestBuildEmailRichTextInterchangeReplacementEmitsDeletes(t *testing.T) {
	firstID, secondID := uuid.NewString(), uuid.NewString()
	base := blockInterchangeBase(paragraphBase(firstID), paragraphBase(secondID))
	source := emailInterchangeDocument("en", base,
		paragraphLocale(firstID, "source first"), paragraphLocale(secondID, "source second"),
	)
	plan := emailInterchangePlan(t, source, "campaign", uuid.NewString(), "ko")
	currentDocument := emailInterchangeDocument("ko", base,
		paragraphLocale(firstID, "old first"), paragraphLocale(secondID, "old second"),
	)
	current, err := projectEmailRichTextInterchangeTarget(
		plan, true, uuid.NewString(), stringPointer("old subject"), currentDocument,
	)
	if err != nil {
		t.Fatalf("project current Email target: %v", err)
	}
	first := "block:" + firstID + ":typed:paragraph/content"
	candidate, err := buildEmailRichTextInterchangeCandidate(application.TranslationInterchangeApply{
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: &core.SourceDocument{
			Title: "source subject", ContentDocumentRevision: uuid.NewString(), ContentBlockDocument: source,
		},
		Plan: plan,
		Targets: map[string]core.UnitResult{
			"entity:title": {UnitID: "entity:title", TranslatedText: "new subject"},
			first:          {UnitID: first, TranslatedText: ""},
		},
	}, current)
	if err != nil {
		t.Fatalf("build Email replacement: %v", err)
	}
	if candidate.Title == nil || *candidate.Title != "new subject" {
		t.Fatalf("replacement subject = %+v", candidate.Title)
	}
	mutations := candidate.RichTextLocaleMutations()
	if len(mutations) != 2 || mutations[0].GetUpsert().GetBlock().GetBlockId() != firstID ||
		mutations[1].GetDelete().GetBlockId() != secondID {
		t.Fatalf("replacement mutations = %+v", mutations)
	}
}

func emailInterchangeDocument(
	locale string,
	base *contentv1.RichTextBlockGraph,
	blocks ...*contentv1.RichTextBlockLocale,
) *contentv1.LocalizedRichTextDocument {
	document := blockInterchangeDocument(locale, base, blocks...)
	document.Profile = contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL
	return document
}

func emailInterchangePlan(
	t *testing.T,
	source *contentv1.LocalizedRichTextDocument,
	entityType string,
	entityID string,
	targetLocale string,
) *core.ExtractionPlan {
	t.Helper()
	plan, err := core.BuildRichTextExtractionPlan(
		&model.TranslationJob{
			EntityType: entityType, EntityID: entityID,
			SourceLocale: source.GetLocale(), TargetLocale: targetLocale,
		},
		&core.SourceDocument{Title: "source subject", ContentBlockDocument: source},
		core.RichTextDocumentFields{Title: true},
	)
	if err != nil {
		t.Fatalf("BuildRichTextExtractionPlan() error = %v", err)
	}
	return plan
}

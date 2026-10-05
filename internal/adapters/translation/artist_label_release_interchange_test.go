package translationadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type creativeInterchangeAuditAppender struct{}

func (creativeInterchangeAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return nil
}

func TestAppendLocaleContentInterchangeAuditSelectsCreateAndUpdateOperation(t *testing.T) {
	buildStopped := errors.New("captured operation")
	for _, test := range []struct {
		name     string
		existed  bool
		expected sharedtelemetry.AuditItemOperation
	}{
		{name: "create", expected: sharedtelemetry.AuditItemOperationCreated},
		{name: "update including exact deletes", existed: true, expected: sharedtelemetry.AuditItemOperationUpdated},
	} {
		t.Run(test.name, func(t *testing.T) {
			var operation sharedtelemetry.AuditItemOperation
			err := appendLocaleContentInterchangeAudit(
				t.Context(), nil, creativeInterchangeAuditAppender{},
				func(
					_ sharedtelemetry.AuditMetadata,
					_ string,
					_ string,
					actual sharedtelemetry.AuditItemOperation,
				) (sharedtelemetry.AuditRecord, error) {
					operation = actual
					return sharedtelemetry.AuditRecord{}, buildStopped
				},
				sharedtelemetry.AuditArtistUpdated,
				uuid.NewString(), uuid.NewString(), "en", test.existed,
			)
			if !errors.Is(err, buildStopped) {
				t.Fatalf("append error = %v, want builder sentinel", err)
			}
			if operation != test.expected {
				t.Fatalf("operation = %q, want %q", operation, test.expected)
			}
		})
	}
}

func TestProjectCreativeInterchangeTargetDistinguishesAbsentAndExplicitEmpty(t *testing.T) {
	blockID := uuid.NewString()
	base := blockInterchangeBase(paragraphBase(blockID))
	source := blockInterchangeDocument("ko", base, paragraphLocale(blockID, "source"))
	plan := blockInterchangePlan(t, source, "artist", uuid.NewString(), "en")
	target := blockInterchangeDocument("en", base, paragraphLocale(blockID, ""))

	absent, err := projectCreativeInterchangeTarget(plan, false, uuid.NewString(), blockInterchangeDocument("en", base))
	if err != nil {
		t.Fatalf("project absent target: %v", err)
	}
	if absent.state.Exists || absent.state.Revision != "" || len(absent.state.Targets) != 0 {
		t.Fatalf("absent target = %+v", absent.state)
	}

	revision := uuid.NewString()
	present, err := projectCreativeInterchangeTarget(plan, true, revision, target)
	if err != nil {
		t.Fatalf("project present target: %v", err)
	}
	handle := "block:" + blockID + ":typed:paragraph/content"
	result, ok := present.state.Targets[handle]
	if !ok || result.TranslatedText != "" || present.state.Revision != revision {
		t.Fatalf("explicit-empty target = (%+v, %v), state=%+v", result, ok, present.state)
	}
}

func TestBuildCreativeInterchangeReplacementSeparatesExplicitEmptyFromOmittedDelete(t *testing.T) {
	firstID, secondID := uuid.NewString(), uuid.NewString()
	base := blockInterchangeBase(paragraphBase(firstID), paragraphBase(secondID))
	source := blockInterchangeDocument("ko", base,
		paragraphLocale(firstID, "source first"), paragraphLocale(secondID, "source second"),
	)
	plan := blockInterchangePlan(t, source, "label", uuid.NewString(), "en")
	current := blockInterchangeDocument("en", base,
		paragraphLocale(firstID, "old first"), paragraphLocale(secondID, "old second"),
	)
	first := "block:" + firstID + ":typed:paragraph/content"

	candidate, err := buildCreativeInterchangeCandidate(application.TranslationInterchangeApply{
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: &core.SourceDocument{
			ContentDocumentRevision: uuid.NewString(), ContentBlockDocument: source,
		},
		Plan: plan,
		Targets: map[string]core.UnitResult{
			first: {UnitID: first, TranslatedText: ""},
		},
	}, current)
	if err != nil {
		t.Fatalf("build replacement: %v", err)
	}
	blocks := richTextLocaleBlocks(candidate.ContentBlockLocaleOverlay)
	if blocks[firstID] == nil || blocks[firstID].GetParagraph() == nil {
		t.Fatalf("explicit-empty first Block = %+v", blocks[firstID])
	}
	for _, inline := range blocks[firstID].GetParagraph().GetContent() {
		if inline.GetText().GetText() != "" {
			t.Fatalf("explicit-empty first Block materialized text %q", inline.GetText().GetText())
		}
	}
	if blocks[secondID] != nil {
		t.Fatalf("omitted Block was synthesized as an upsert: %+v", blocks[secondID])
	}
	if len(candidate.ContentBlockLocaleDeletes) != 1 || candidate.ContentBlockLocaleDeletes[0] != secondID {
		t.Fatalf("exact replacement deletes = %v, want [%s]", candidate.ContentBlockLocaleDeletes, secondID)
	}
}

func TestBuildReleaseInterchangePatchPreservesAggregateSiblings(t *testing.T) {
	firstID, secondID := uuid.NewString(), uuid.NewString()
	base := blockInterchangeBase(paragraphBase(firstID), paragraphBase(secondID))
	source := blockInterchangeDocument("ko", base,
		paragraphLocale(firstID, "source first"), paragraphLocale(secondID, "source second"),
	)
	plan := blockInterchangePlan(t, source, "release", uuid.NewString(), "en")
	plan.Units = append(plan.Units,
		core.Unit{UnitID: "credit-note:credit-a"},
		core.Unit{UnitID: "credit-note:credit-b"},
	)
	currentDocument := blockInterchangeDocument("en", base,
		paragraphLocale(firstID, "old first"), paragraphLocale(secondID, "old second"),
	)
	first := "block:" + firstID + ":typed:paragraph/content"
	currentProjected, err := ProjectRichTextInterchangeTargets(plan, currentDocument)
	if err != nil {
		t.Fatalf("project current target: %v", err)
	}
	current := releaseInterchangeTarget{
		creativeInterchangeTarget: creativeInterchangeTarget{
			state:     application.TranslationInterchangeTargetState{Exists: true, Targets: currentProjected},
			localized: currentDocument,
		},
		creditNotes: map[string]string{"credit-a": "old a", "credit-b": "old b"},
	}

	candidate, err := buildReleaseInterchangeCandidate(application.TranslationInterchangeApply{
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: &core.SourceDocument{
			ContentDocumentRevision: uuid.NewString(), ContentBlockDocument: source,
		},
		Plan: plan,
		Targets: map[string]core.UnitResult{
			first:                  {UnitID: first, TranslatedText: "new first"},
			"credit-note:credit-a": {UnitID: "credit-note:credit-a", TranslatedText: ""},
		},
	}, current)
	if err != nil {
		t.Fatalf("build Release patch: %v", err)
	}
	blocks := richTextLocaleBlocks(candidate.ContentBlockLocaleOverlay)
	if got := blocks[firstID].GetParagraph().GetContent()[0].GetText().GetText(); got != "new first" {
		t.Fatalf("patched first = %q", got)
	}
	if got := blocks[secondID].GetParagraph().GetContent()[0].GetText().GetText(); got != "old second" {
		t.Fatalf("untouched Release Block = %q", got)
	}
	if got := candidate.ReleaseCreditNotes["credit-a"]; got != "" {
		t.Fatalf("explicit-empty credit a = %q", got)
	}
	if got := candidate.ReleaseCreditNotes["credit-b"]; got != "old b" {
		t.Fatalf("untouched credit b = %q", got)
	}
}

package emailauthoring

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateEmailLayoutAIDocumentMutationKeepsLocaleLifecycleExclusive(t *testing.T) {
	t.Parallel()
	base := EmailLayoutAIDocumentMutation{
		LayoutID: uuid.NewString(), Locale: "ko", ExpectedSource: "en",
		ExpectedDocumentRevision: "eld1_revision", ContributorMemberID: uuid.New(),
	}
	tests := []struct {
		name  string
		input EmailLayoutAIDocumentMutation
		valid bool
	}{
		{name: "implicit target create with explicit empty", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.ReplaceValues = true
			value.Values = map[string]string{"unit:" + uuid.NewString() + ":text": ""}
			return value
		}(), valid: true},
		{name: "create absent target", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.CreateTranslation = true
			return value
		}(), valid: true},
		{name: "delete existing target", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.ExpectedPresence = true
			targetRevision := "tr1_target"
			value.ExpectedTargetRevision = &targetRevision
			value.DeleteTranslation = true
			return value
		}(), valid: true},
		{name: "create source", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.Locale = "en"
			value.CreateTranslation = true
			return value
		}()},
		{name: "delete absent target", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.DeleteTranslation = true
			return value
		}()},
		{name: "mixed modes", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.ReplaceValues = true
			value.CreateTranslation = true
			return value
		}()},
		{name: "missing mode", input: base},
		{name: "non-canonical target locale", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.Locale = "ko_KR"
			value.ReplaceValues = true
			return value
		}()},
		{name: "non-canonical source locale", input: func() EmailLayoutAIDocumentMutation {
			value := base
			value.ExpectedSource = "EN"
			value.ReplaceValues = true
			return value
		}()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateEmailLayoutAIDocumentMutation(test.input)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}

func TestEmailLayoutAIDocumentSplitRevisionsIsolateTargetWrites(t *testing.T) {
	t.Parallel()

	updated := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	base := EmailLayoutAIDocumentState{
		DocumentRevision: uuid.NewString(),
		SourceLocale:     "en", Locale: "ko", LocaleExists: true,
		localeUpdatedAt: &updated,
	}
	documentRevision := base.DocumentRevision
	targetRevision, err := deriveEmailLayoutAIDocumentTargetRevision(base)
	if err != nil || targetRevision == nil {
		t.Fatalf("target revision = %v, %v", targetRevision, err)
	}

	targetChanged := base
	nextTargetTime := updated.Add(time.Microsecond)
	targetChanged.localeUpdatedAt = &nextTargetTime
	nextDocumentRevision := targetChanged.DocumentRevision
	if nextDocumentRevision != documentRevision {
		t.Fatalf("target write changed document revision: %q", nextDocumentRevision)
	}
	targetChanged.DocumentRevision = nextDocumentRevision
	nextTargetRevision, err := deriveEmailLayoutAIDocumentTargetRevision(targetChanged)
	if err != nil || nextTargetRevision == nil || *nextTargetRevision == *targetRevision {
		t.Fatalf("target write did not change target revision: %v, %v", nextTargetRevision, err)
	}

	sourceChanged := base
	sourceChanged.DocumentRevision = uuid.NewString()
	nextDocumentRevision = sourceChanged.DocumentRevision
	if nextDocumentRevision == documentRevision {
		t.Fatalf("source write did not change document revision: %q", nextDocumentRevision)
	}
	sourceChanged.DocumentRevision = nextDocumentRevision
	nextTargetRevision, err = deriveEmailLayoutAIDocumentTargetRevision(sourceChanged)
	if err != nil || nextTargetRevision == nil || *nextTargetRevision == *targetRevision {
		t.Fatalf("source write did not invalidate target revision: %v, %v", nextTargetRevision, err)
	}

	missing := base
	missing.LocaleExists = false
	missing.localeUpdatedAt = nil
	missing.DocumentRevision = documentRevision
	missingTargetRevision, err := deriveEmailLayoutAIDocumentTargetRevision(missing)
	if err != nil || missingTargetRevision != nil {
		t.Fatalf("missing target revision = %v, %v", missingTargetRevision, err)
	}

	source := base
	source.Locale = source.SourceLocale
	source.DocumentRevision = documentRevision
	sourceTargetRevision, err := deriveEmailLayoutAIDocumentTargetRevision(source)
	if err != nil || sourceTargetRevision != nil {
		t.Fatalf("source target revision = %v, %v", sourceTargetRevision, err)
	}
}

func TestEmailLayoutLocaleValuesPreservesExplicitEmpty(t *testing.T) {
	t.Parallel()

	empty := ""
	state := EmailLayoutAIDocumentState{Units: []EmailLayoutAIDocumentUnit{
		{Handle: "unit:" + uuid.NewString() + ":text", LocaleValue: &empty},
		{Handle: "unit:" + uuid.NewString() + ":text"},
	}}
	values := emailLayoutLocaleValues(state)
	if len(values) != 1 || values[state.Units[0].Handle] != "" {
		t.Fatalf("explicit empty/absent values = %#v", values)
	}
}

package label

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

func TestValidateLabelTargetBatchReservesLocaleDeletesForProviderReplacement(t *testing.T) {
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{blockID}}},
	}
	err := validateLabelTargetBatch(batch, documentID, revision, "ko", false)
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "explicit empty") {
		t.Fatalf("interactive locale delete error = %v", err)
	}
	if err := validateLabelTargetBatch(batch, documentID, revision, "ko", true); err != nil {
		t.Fatalf("provider replacement locale delete rejected: %v", err)
	}
}

func TestValidateCompiledLabelAIDocumentMutationKeepsLockedStateAuthoritative(t *testing.T) {
	labelID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision := "tr1_label-target"
	state := AIDocumentState{
		LabelID: labelID.String(), ContentDocumentID: documentID, Revision: revision.String(),
		SourceLocale: "en", Locale: "ko", LocaleExists: true,
		TargetRevision: &targetRevision,
		ViewerMemberID: contributor.String(),
	}
	valid := AIDocumentMutation{
		LabelID: state.LabelID, Locale: state.Locale, ExpectedRevision: state.Revision,
		ExpectedTargetRevision: &targetRevision,
		ExpectedSource:         state.SourceLocale, ExpectedPresence: state.LocaleExists,
		ContributorMemberID: contributor,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
	}
	resolved, err := validateCompiledLabelAIDocumentMutation(state, valid, contributor)
	if err != nil || resolved != revision {
		t.Fatalf("valid compiled mutation = (%s, %v), want %s", resolved, err, revision)
	}

	tests := []struct {
		name   string
		mutate func(*AIDocumentMutation)
	}{
		{name: "Label", mutate: func(m *AIDocumentMutation) { m.LabelID = uuid.NewString() }},
		{name: "locale", mutate: func(m *AIDocumentMutation) { m.Locale = "fr" }},
		{name: "source", mutate: func(m *AIDocumentMutation) { m.ExpectedSource = "fr" }},
		{name: "presence", mutate: func(m *AIDocumentMutation) { m.ExpectedPresence = false }},
		{name: "contributor", mutate: func(m *AIDocumentMutation) { m.ContributorMemberID = uuid.New() }},
		{name: "document", mutate: func(m *AIDocumentMutation) { m.Batch.DocumentID = uuid.New() }},
		{name: "batch revision", mutate: func(m *AIDocumentMutation) { m.Batch.ExpectedRevision = uuid.New() }},
		{name: "batch contributor", mutate: func(m *AIDocumentMutation) { m.Batch.ContributorMemberIDs = []uuid.UUID{uuid.New()} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutation := valid
			batch := *valid.Batch
			batch.ContributorMemberIDs = append([]uuid.UUID(nil), valid.Batch.ContributorMemberIDs...)
			mutation.Batch = &batch
			test.mutate(&mutation)
			_, err := validateCompiledLabelAIDocumentMutation(state, mutation, contributor)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("mismatched compiled mutation error = %v", err)
			}
		})
	}

	stale := valid
	stale.ExpectedRevision = uuid.NewString()
	stale.Batch = nil
	stale.DeleteTranslation = true
	_, err = validateCompiledLabelAIDocumentMutation(state, stale, contributor)
	var conflict *AIDocumentRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Kind != AIDocumentDocumentRevisionConflict || conflict.CurrentDocumentRevision != state.Revision {
		t.Fatalf("stale compiled mutation error = %v", err)
	}

	staleTarget := valid
	staleTargetRevision := "tr1_stale"
	staleTarget.ExpectedTargetRevision = &staleTargetRevision
	_, err = validateCompiledLabelAIDocumentMutation(state, staleTarget, contributor)
	if !errors.As(err, &conflict) || conflict.Kind != AIDocumentTargetRevisionConflict || conflict.CurrentTargetRevision == nil || *conflict.CurrentTargetRevision != targetRevision {
		t.Fatalf("stale target mutation error = %v", err)
	}
}

func TestLabelAIDocumentLifecycleUsesExactGeneratedStatus(t *testing.T) {
	tests := []struct {
		status string
		valid  bool
	}{
		{status: managev1.LabelStatus_LABEL_STATUS_DRAFT.String(), valid: true},
		{status: managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String(), valid: true},
		{status: "PUBLISHED"},
		{status: "LABEL_STATUS_PUBLISHED "},
		{status: "LABEL_STATUS_ARCHIVED"},
		{status: managev1.LabelStatus_LABEL_STATUS_UNSPECIFIED.String()},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			if got := labelAIDocumentLifecycleValid(test.status); got != test.valid {
				t.Fatalf("labelAIDocumentLifecycleValid(%q) = %t, want %t", test.status, got, test.valid)
			}
		})
	}
}

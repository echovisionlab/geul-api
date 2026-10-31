package aidocumentadapter

import (
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/stretchr/testify/require"
)

func TestHandleExactMutationValidationErrorPreservesValidateAndApplyShape(t *testing.T) {
	validation := core.ValidationResult{
		Normalized: []core.Operation{core.SetFieldOperation("root", "title", core.Text("Title"))},
		Issues: []core.OperationIssue{{
			Operation: 0, Code: core.IssueInvalidOperation, Message: "domain rule",
		}},
	}
	rejected := rejectExactMutation("Post", validation)

	result, err, handled := handleExactMutationValidationError(rejected, true)
	require.True(t, handled)
	require.NoError(t, err)
	require.Equal(t, validation, result)

	result, err, handled = handleExactMutationValidationError(rejected, false)
	require.True(t, handled)
	require.Empty(t, result)
	var validationError *core.ValidationError
	require.ErrorAs(t, err, &validationError)
	require.Equal(t, validation, validationError.Result)

	conflict := core.Conflict{
		Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "revision-2",
	}
	rejected = rejectExactMutation("Post", core.ValidationResult{Conflict: &conflict})
	_, err, handled = handleExactMutationValidationError(rejected, false)
	require.True(t, handled)
	var conflictError *core.ConflictError
	require.ErrorAs(t, err, &conflictError)
	require.Equal(t, conflict, conflictError.Conflict)

	_, err, handled = handleExactMutationValidationError(errors.New("domain unavailable"), true)
	require.False(t, handled)
	require.NoError(t, err)
}

func TestExactMutationRunRejectsDomainIssuesAndBindsAcceptedCommand(t *testing.T) {
	run := newExactMutationRun("Post")
	run.command = core.ValidatedApply{
		ExpectedDocumentRevision: "revision-1",
		ExpectedTargetRevision:   adapterRevisionPointer("target-1"),
		Operations: []core.Operation{
			core.SetFieldOperation("root", "title", core.Text("Title")),
		},
	}
	run.validation = core.ValidationResult{Normalized: append([]core.Operation(nil), run.command.Operations...)}

	rejected := run.rejectIssues([]core.OperationIssue{{
		Operation: 0, Code: core.IssueInvalidOperation, Message: "domain rule",
	}})
	var validationError *exactMutationValidationError
	require.ErrorAs(t, rejected, &validationError)
	require.Equal(t, run.validation, validationError.result)

	run.validation.Issues = nil
	accepted, err := run.accept(core.ApplyResult{
		DocumentRevision: "revision-1", TargetRevision: adapterRevisionPointer("target-2"), Changed: true,
		Changes: []core.Change{{Operation: 0, Kind: core.OperationSetField}},
	})
	require.NoError(t, err)
	require.Equal(t, run.command.Operations, accepted.Normalized)
}

func adapterRevisionPointer(value core.Revision) *core.Revision { return &value }

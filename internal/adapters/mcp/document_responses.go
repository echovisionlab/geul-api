package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func encodeValidation(validation core.ValidationResult) ([]byte, error) {
	output := make(map[string]any, 3)
	if len(validation.Normalized) != 0 {
		for _, operation := range validation.Normalized {
			if _, err := json.Marshal(operation); err != nil {
				return nil, fmt.Errorf("invalid normalized operation: %w", err)
			}
		}
		output["o"] = validation.Normalized
	}
	if len(validation.Issues) != 0 {
		issues := make([][4]any, 0, len(validation.Issues))
		for _, issue := range validation.Issues {
			if issue.Operation < 0 {
				return nil, errors.New("application returned a negative validation operation index")
			}
			if !supportedIssueCode(issue.Code) {
				return nil, fmt.Errorf("application returned unsupported validation issue code %q", issue.Code)
			}
			issues = append(issues, [4]any{issue.Operation, issue.Code, issue.Handle, issue.Message})
		}
		output["i"] = issues
	}
	if validation.Conflict != nil {
		if validation.Conflict.Code != core.ConflictDocumentRevision &&
			validation.Conflict.Code != core.ConflictTargetRevision {
			return nil, fmt.Errorf("application returned unsupported conflict code %q", validation.Conflict.Code)
		}
		if validation.Conflict.CurrentDocumentRevision == "" {
			return nil, errors.New("application returned a conflict without current document revision")
		}
		output["x"] = [4]any{
			validation.Conflict.Code,
			validation.Conflict.CurrentDocumentRevision,
			validation.Conflict.CurrentTargetRevision,
			append([]string{}, validation.Conflict.AffectedHandles...),
		}
	}
	return json.Marshal(output)
}

// encodeFocusedAccepted validates the application result before extending its
// wire object, so focused mutations share the same fail-closed checks as apply.
func encodeFocusedAccepted(result core.ApplyResult, createdBlock core.BlockID) ([]byte, error) {
	output, err := acceptedOutput(result)
	if err != nil {
		return nil, err
	}
	if createdBlock != "" {
		output["block_id"] = createdBlock
	}
	return json.Marshal(output)
}

func acceptedOutput(result core.ApplyResult) (map[string]any, error) {
	if result.DocumentRevision == "" {
		return nil, errors.New("application returned an accepted mutation without document revision")
	}
	changes := make([][3]any, 0, len(result.Changes))
	for _, change := range result.Changes {
		if change.Operation < 0 {
			return nil, errors.New("application returned a negative accepted operation index")
		}
		if !supportedOperationKind(change.Kind) {
			return nil, fmt.Errorf("application returned unsupported accepted operation kind %q", change.Kind)
		}
		changes = append(changes, [3]any{
			change.Operation,
			change.Kind,
			append([]string{}, change.AffectedHandles...),
		})
	}
	output := map[string]any{"dr": result.DocumentRevision, "c": changes}
	if result.TargetRevision != nil {
		output["tr"] = result.TargetRevision
	}
	return output, nil
}

func supportedOperationKind(kind core.OperationKind) bool {
	switch kind {
	case core.OperationSetField, core.OperationUnsetField,
		core.OperationInsertBlock, core.OperationDeleteBlock,
		core.OperationMoveBlock, core.OperationReplaceBlockKind,
		core.OperationInsertRelationItem, core.OperationDeleteRelationItem,
		core.OperationMoveRelationItem, core.OperationAttachFile,
		core.OperationDetachFile, core.OperationCreateTranslation,
		core.OperationDeleteTranslation:
		return true
	default:
		return false
	}
}

func supportedIssueCode(code core.IssueCode) bool {
	switch code {
	case core.IssueInvalidOperation, core.IssueUnknownBlock,
		core.IssueDuplicateBlock, core.IssueUnknownBlockKind,
		core.IssueUnknownField, core.IssueUnknownRelation,
		core.IssueUnknownRelationItem, core.IssueDuplicateRelationItem,
		core.IssueInvalidRelationItemMove, core.IssueValueKindMismatch,
		core.IssueSourceAuthorityRequired, core.IssueTargetFieldForbidden,
		core.IssueInvalidBlockRelation, core.IssueBlockCycle,
		core.IssueInvalidFileReference, core.IssueTranslationIsSource,
		core.IssueTranslationAlreadyExists, core.IssueTranslationMissing,
		core.IssueLocaleOperationNotExclusive:
		return true
	default:
		return false
	}
}

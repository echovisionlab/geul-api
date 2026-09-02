package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func validateBlockInterchangeApply(command application.TranslationInterchangeApply, entityType string) error {
	if command.EntityType != entityType || command.Plan == nil || command.Source == nil ||
		command.Source.ContentBlockDocument == nil || command.Plan.EntityType != command.EntityType ||
		command.Plan.EntityID != command.EntityID || command.Plan.SourceLocale != command.SourceLocale ||
		command.Plan.TargetLocale != command.TargetLocale {
		return errs.InvalidArgument("target", fmt.Sprintf("%s translation interchange identity does not match the validated source", entityType))
	}
	if command.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		command.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	return nil
}

func requireTranslationInterchangeRevision(
	state application.TranslationInterchangeTargetState,
	expected *string,
) error {
	if expected == nil {
		if state.Exists {
			return connect.NewError(connect.CodeAborted, errors.New("translation target was created; reload before importing"))
		}
		return nil
	}
	if !state.Exists || state.Revision != *expected {
		return connect.NewError(connect.CodeAborted, errors.New("translation target revision changed; reload before importing"))
	}
	return nil
}

func buildBlockInterchangeCandidate(
	command application.TranslationInterchangeApply,
	current *contentv1.LocalizedRichTextDocument,
	targets map[string]core.UnitResult,
) (*core.Candidate, error) {
	if command.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return core.BuildRichTextCandidate(command.Plan, command.Source, targets)
	}
	overlay, err := buildBlockInterchangePatch(
		command.Plan,
		command.Source.ContentBlockDocument,
		current,
		command.Targets,
	)
	if err != nil {
		return nil, err
	}
	return &core.Candidate{
		ContentBlockLocaleOverlay: overlay,
		ContentDocumentRevision:   command.Source.ContentDocumentRevision,
	}, nil
}

func interchangeCandidateTargets(
	mode managev1.TranslationInterchangeMode,
	current map[string]core.UnitResult,
	imported map[string]core.UnitResult,
) map[string]core.UnitResult {
	capacity := len(imported)
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
		capacity += len(current)
	}
	result := make(map[string]core.UnitResult, capacity)
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
		for handle, value := range current {
			result[handle] = value
		}
	}
	for handle, value := range imported {
		result[handle] = value
	}
	return result
}

func addEntityInterchangeTarget(
	targets map[string]core.UnitResult,
	plan *core.ExtractionPlan,
	field string,
	value *string,
) {
	handle := "entity:" + field
	if value == nil || !interchangePlanHasUnit(plan, handle) {
		return
	}
	targets[handle] = core.UnitResult{UnitID: handle, TranslatedText: *value}
}

func entityInterchangeTarget(
	targets map[string]core.UnitResult,
	plan *core.ExtractionPlan,
	field string,
) *string {
	handle := "entity:" + field
	if !interchangePlanHasUnit(plan, handle) {
		return nil
	}
	value, ok := targets[handle]
	if !ok {
		return nil
	}
	result := value.TranslatedText
	return &result
}

func interchangePlanHasUnit(plan *core.ExtractionPlan, handle string) bool {
	if plan == nil {
		return false
	}
	for _, unit := range plan.Units {
		if unit.UnitID == handle {
			return true
		}
	}
	return false
}

func translationInterchangeRequesterMemberID(ctx context.Context) (string, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || strings.TrimSpace(principal.MemberID.String()) == "" {
		return "", errs.AuthenticationRequired()
	}
	return principal.MemberID.String(), nil
}

func canonicalInterchangeDocumentID(value *string) (uuid.UUID, error) {
	if value == nil {
		return uuid.Nil, errors.New("content document identity is missing")
	}
	normalized := strings.TrimSpace(*value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, errors.New("content document identity is invalid")
	}
	return parsed, nil
}

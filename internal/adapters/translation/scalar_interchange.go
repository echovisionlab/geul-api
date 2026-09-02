package translationadapter

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

func validateScalarInterchangeApply(
	command application.TranslationInterchangeApply,
	entityType string,
) error {
	if command.EntityType != entityType || command.Plan == nil || command.Source == nil ||
		command.Plan.EntityType != command.EntityType || command.Plan.EntityID != command.EntityID ||
		command.Plan.SourceLocale != command.SourceLocale ||
		command.Plan.TargetLocale != command.TargetLocale {
		return errs.InvalidArgument(
			"target",
			fmt.Sprintf("%s translation interchange identity does not match the validated source", entityType),
		)
	}
	return nil
}

func mapScalarInterchangeDomainError(err error) error {
	if err == nil {
		return nil
	}
	var conflict *core.TargetRevisionConflict
	if errors.As(err, &conflict) {
		return connect.NewError(connect.CodeAborted, err)
	}
	return err
}

package programevent

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

type programEventSourceState struct {
	SourceLocale string
}

func loadProgramEventSourceLocale(
	ctx context.Context,
	store Store,
	eventID string,
	lock bool,
) (programEventSourceState, error) {
	rowLock := NoLock
	if lock {
		rowLock = UpdateLock
	}
	sourceLocale, err := store.SourceLocale(ctx, eventID, rowLock)
	if err != nil {
		return programEventSourceState{}, notFoundAs(err, domainerr.Precondition("Program Event translation source is not initialized"))
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return programEventSourceState{}, fmt.Errorf("program Event %s has invalid source locale", eventID)
	}
	return programEventSourceState{SourceLocale: sourceLocale}, nil
}

type programEventTranslationSourceMetadata struct {
	Title   string
	Summary *string
}

func loadTranslationSourceMetadata(
	ctx context.Context,
	store Store,
	eventID string,
	sourceLocale string,
) (programEventTranslationSourceMetadata, error) {
	title, summary, found, err := store.TranslationSource(ctx, eventID, sourceLocale)
	if err != nil {
		return programEventTranslationSourceMetadata{}, err
	}
	if !found {
		return programEventTranslationSourceMetadata{}, domainerr.Precondition("Program Event source locale metadata is not initialized")
	}
	return programEventTranslationSourceMetadata{Title: title, Summary: summary}, nil
}

func loadProgramEventBlockLocaleMetadata(
	ctx context.Context,
	store Store,
	eventID string,
	sourceLocale string,
) (*BlockLocaleMetadata, error) {
	title, err := store.Title(ctx, eventID)
	if err != nil {
		return nil, notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
	}
	summary, found, err := store.LocaleSummary(ctx, eventID, sourceLocale)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, domainerr.Precondition("Program Event source locale metadata is not initialized")
	}
	return &BlockLocaleMetadata{
		Locale:  sourceLocale,
		Title:   &title,
		Summary: summary,
	}, nil
}

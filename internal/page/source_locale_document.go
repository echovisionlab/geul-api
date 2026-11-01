package page

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

func loadRequiredPageSourceLocaleMetadata(ctx context.Context, store Store, pageID string) (SourceMetadata, error) {
	states, err := store.SourceMetadata(ctx, []string{pageID})
	if err != nil {
		return SourceMetadata{}, err
	}
	state, ok := states[pageID]
	if !ok {
		return SourceMetadata{}, domainerr.ResourceNotFound("page_translation", pageID)
	}
	return state, nil
}

func createInitialPageSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	pageID string,
	sourceLocale string,
	title string,
	summary *string,
	now time.Time,
) error {
	if strings.TrimSpace(sourceLocale) == "" {
		return errors.New("invalid initial Page source locale")
	}
	return store.InsertLocale(ctx, pageID, sourceLocale, &title, summary, now)
}

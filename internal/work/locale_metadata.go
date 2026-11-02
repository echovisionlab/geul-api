package work

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// LoadRequiredSourceLocaleMetadata loads the Work-owned source-locale row,
// through store bound to the caller's transaction.
func LoadRequiredSourceLocaleMetadata(ctx context.Context, store Store, workID string) (LocaleMetadata, error) {
	states, err := store.SourceMetadata(ctx, []string{workID})
	if err != nil {
		return LocaleMetadata{}, err
	}
	state, ok := states[workID]
	if !ok {
		return LocaleMetadata{}, domainerr.ResourceNotFound("work_translation", workID)
	}
	return state, nil
}

func createInitialWorkSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	workID string,
	sourceLocale string,
	title string,
	summary *string,
	now time.Time,
) error {
	if strings.TrimSpace(sourceLocale) == "" {
		return errors.New("invalid initial Work source locale")
	}
	return store.InsertSourceLocale(ctx, workID, sourceLocale, title, summary, now)
}

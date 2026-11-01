package series

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/google/uuid"
)

const seriesContentDocumentProfile = "compact"

type seriesContentDocumentState struct {
	ID           uuid.UUID
	Revision     uuid.UUID
	SourceLocale string
}

func loadSeriesContentDocumentState(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
	lock bool,
) (seriesContentDocumentState, error) {
	root, err := store.ContentDocumentRoot(ctx, seriesID, lock)
	if err != nil {
		if errors.Is(err, ErrSeriesNotFound) {
			return seriesContentDocumentState{}, domainerr.ResourceNotFound("series", seriesID)
		}
		return seriesContentDocumentState{}, err
	}
	if root.ContentDocumentID == nil || strings.TrimSpace(*root.ContentDocumentID) == "" {
		return seriesContentDocumentState{}, domainerr.Precondition("series content document is not initialized")
	}
	documentID, err := parseSeriesContentDocumentUUID(*root.ContentDocumentID, "content_document_id")
	if err != nil {
		return seriesContentDocumentState{}, err
	}

	document, err := store.ContentDocument(ctx, documentID.String(), lock)
	if err != nil {
		if errors.Is(err, ErrContentDocumentNotFound) {
			return seriesContentDocumentState{}, domainerr.Precondition("series content document is missing")
		}
		return seriesContentDocumentState{}, err
	}
	if strings.TrimSpace(document.Profile) != seriesContentDocumentProfile {
		return seriesContentDocumentState{}, domainerr.Precondition("series content document profile is invalid")
	}
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return seriesContentDocumentState{}, domainerr.Precondition("series source locale is not initialized")
	}
	revision, err := parseSeriesContentDocumentUUID(derefString(document.Revision), "content_document.revision")
	if err != nil {
		return seriesContentDocumentState{}, err
	}
	return seriesContentDocumentState{
		ID:           documentID,
		Revision:     revision,
		SourceLocale: sourceLocale,
	}, nil
}

func parseSeriesContentDocumentUUID(value, field string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, fmt.Errorf("invalid Series %s %q", field, normalized)
	}
	return parsed, nil
}

func createSeriesContentDocument(ctx context.Context, store Store, now time.Time) (uuid.UUID, error) {
	documentID := uuid.New()
	revision := uuid.New()
	if err := store.CreateContentDocument(ctx, documentID.String(), revision.String(), now.UTC()); err != nil {
		return uuid.Nil, err
	}
	return documentID, nil
}

func advanceSeriesContentDocument(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	now time.Time,
) (uuid.UUID, error) {
	if documentID == uuid.Nil || expectedRevision == uuid.Nil {
		return uuid.Nil, domainerr.InvalidField("expected_document_revision", "Series content document identity and revision are required")
	}
	nextRevision := uuid.New()
	err := store.AdvanceContentDocument(ctx, seriesID, documentID.String(), expectedRevision.String(), nextRevision.String(), now.UTC())
	if errors.Is(err, ErrContentDocumentRevisionChanged) {
		return uuid.Nil, domainerr.Precondition("series content document revision changed; reload before saving")
	}
	if err != nil {
		return uuid.Nil, err
	}
	return nextRevision, nil
}

func advanceSeriesContentDocumentForMutation(ctx context.Context, store Store, seriesID string, now time.Time) error {
	state, err := loadSeriesContentDocumentState(ctx, store, seriesID, false)
	if err != nil {
		return err
	}
	_, err = advanceSeriesContentDocument(ctx, store, seriesID, state.ID, state.Revision, now)
	return err
}

func deleteSeriesContentDocument(ctx context.Context, store Store, documentID uuid.UUID) error {
	if documentID == uuid.Nil {
		return domainerr.InvalidField("content_document_id", "Series content document is required")
	}
	err := store.DeleteContentDocument(ctx, documentID.String())
	if errors.Is(err, ErrContentDocumentNotFound) {
		return domainerr.Precondition("series content document is missing")
	}
	return err
}

package menu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/google/uuid"
)

const menuContentDocumentProfile = "compact"

type menuContentDocumentState struct {
	ID           uuid.UUID
	Revision     uuid.UUID
	SourceLocale string
}

func loadMenuContentDocumentState(
	ctx context.Context,
	store Store,
	root Root,
	lock bool,
) (menuContentDocumentState, error) {
	documentID, err := parseMenuContentDocumentUUID(root.ContentDocumentID, "content_document_id")
	if err != nil {
		return menuContentDocumentState{}, err
	}
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return menuContentDocumentState{}, domainerr.Precondition("Menu source locale is not initialized")
	}

	read := store.ContentDocument
	if lock {
		read = store.LockContentDocument
	}
	document, err := read(ctx, documentID)
	if err != nil {
		if errors.Is(err, ErrContentDocumentNotFound) {
			return menuContentDocumentState{}, domainerr.Precondition("Menu content document is missing")
		}
		return menuContentDocumentState{}, err
	}
	if strings.TrimSpace(document.Profile) != menuContentDocumentProfile {
		return menuContentDocumentState{}, domainerr.Precondition("Menu content document profile is invalid")
	}
	revision, err := parseMenuContentDocumentUUID(document.Revision, "content_document.revision")
	if err != nil {
		return menuContentDocumentState{}, err
	}
	return menuContentDocumentState{ID: documentID, Revision: revision, SourceLocale: sourceLocale}, nil
}

func parseMenuContentDocumentUUID(value, field string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid Menu %s: %s: %w", field, normalized, err)
	}
	if parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, fmt.Errorf("invalid Menu %s: %s", field, normalized)
	}
	return parsed, nil
}

func createMenuContentDocument(ctx context.Context, store Store, now time.Time) (uuid.UUID, error) {
	documentID := uuid.New()
	if err := store.InsertContentDocument(ctx, documentID, menuContentDocumentProfile, uuid.New(), now.UTC()); err != nil {
		return uuid.Nil, err
	}
	return documentID, nil
}

func advanceMenuContentDocument(
	ctx context.Context,
	store Store,
	menuID string,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	now time.Time,
) error {
	if documentID == uuid.Nil || expectedRevision == uuid.Nil {
		return domainerr.InvalidField(
			"expected_document_revision",
			"Menu content document identity and revision are required",
		)
	}
	err := store.AdvanceContentDocument(ctx, menuID, documentID, expectedRevision, uuid.New(), now.UTC())
	if errors.Is(err, ErrRevisionChanged) {
		return domainerr.Precondition("Menu content document revision changed; reload before saving")
	}
	return err
}

func deleteMenuContentDocument(ctx context.Context, store Store, documentID uuid.UUID) error {
	if documentID == uuid.Nil {
		return domainerr.InvalidField("content_document_id", "Menu content document is required")
	}
	err := store.DeleteContentDocument(ctx, documentID)
	if errors.Is(err, ErrContentDocumentNotFound) {
		return domainerr.Precondition("Menu content document is missing")
	}
	return err
}

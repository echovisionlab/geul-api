package work

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/google/uuid"
)

// switchWorkVersionRestoreSourceLocale changes the Work-owned source-locale
// pointer under the same content-document CAS used by the restore. The
// owning root is the only source-locale authority; no translation lifecycle
// row is created or updated here.
func switchWorkVersionRestoreSourceLocale(
	ctx context.Context,
	store Store,
	workID string,
	requestedLocale string,
	expectedDocumentRevision uuid.UUID,
	previousLocale string,
	now time.Time,
) error {
	if store == nil {
		return domainerr.Precondition("Work content document is not initialized")
	}
	if strings.TrimSpace(workID) == "" || strings.TrimSpace(requestedLocale) == "" ||
		strings.TrimSpace(previousLocale) == "" || expectedDocumentRevision == uuid.Nil {
		return domainerr.InvalidField("source_locale", "source-locale switch identity is required")
	}
	if previousLocale == requestedLocale {
		return domainerr.Precondition("requested locale is already the source locale")
	}
	root, err := store.ContentRoot(ctx, workID, UpdateLock)
	if errors.Is(err, ErrWorkNotFound) {
		return domainerr.ResourceNotFound("work", workID)
	}
	if err != nil {
		return err
	}
	documentID := uuid.Nil
	if root.ContentDocumentID != nil {
		if documentID, err = uuid.Parse(*root.ContentDocumentID); err != nil {
			return err
		}
	}
	if documentID == uuid.Nil {
		return domainerr.Precondition("Work content document is not initialized")
	}
	if root.SourceLocale != previousLocale {
		return domainerr.Precondition("Work source locale changed during version restore")
	}
	_, err = store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedDocumentRevision},
		func(context.Context, Store, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: previousLocale}, nil
		},
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			if err := store.SwitchSourceLocale(ctx, workID, previousLocale, requestedLocale, now); err != nil {
				if errors.Is(err, ErrRowChanged) {
					return contentblock.MetadataEffect{}, domainerr.Precondition("Work source locale changed during version restore")
				}
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{
				Changed:                  true,
				AffectsTranslationSource: true,
				SourceLocale:             requestedLocale,
			}, nil
		},
	)
	return err
}

package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// loadEmailLayoutDocumentAuthority reads the layout root with its content
// document revision, locking the root with lock.
func loadEmailLayoutDocumentAuthority(
	ctx context.Context,
	store Store,
	layoutID string,
	lock emailcontent.RowLock,
) (LayoutAuthority, error) {
	if strings.TrimSpace(layoutID) == "" {
		return LayoutAuthority{}, domainerr.InvalidField("email_layout_id", "is required")
	}
	authority, err := store.LayoutAuthority(ctx, layoutID, lock)
	if errors.Is(err, ErrLayoutNotFound) {
		return LayoutAuthority{}, domainerr.ResourceNotFound("email layout", layoutID)
	}
	if err != nil {
		return LayoutAuthority{}, err
	}
	if authority.DocumentID == uuid.Nil || authority.DocumentRevision == uuid.Nil || strings.TrimSpace(authority.SourceLocale) == "" {
		return LayoutAuthority{}, domainerr.Precondition("Email Layout Content Document authority is not initialized")
	}
	return authority, nil
}

func parseEmailLayoutDocumentRevision(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	revision, err := uuid.Parse(value)
	if err != nil || revision == uuid.Nil || revision.String() != value {
		return uuid.Nil, domainerr.InvalidField("expected_document_revision", "must be a canonical UUID")
	}
	return revision, nil
}

// emailLayoutContentFence is the fence of a layout content write: the layout
// keeps its document, and no active delivery renders it.
func emailLayoutContentFence(store Store, layoutID string) emailcontent.Fence {
	return func(ctx context.Context, documentID uuid.UUID) (contentblock.DomainContext, error) {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, store, layoutID, emailcontent.UpdateLock)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if authority.DocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Email Layout Content Document changed")
		}
		if err := store.RequireLayoutMutable(ctx, layoutID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: authority.SourceLocale}, nil
	}
}

func deriveEmailLayoutTargetRevision(
	documentRevision string,
	entry *emailutil.LayoutTranslationEntry,
) (*string, error) {
	if entry == nil {
		return nil, nil
	}
	updatedAt := entry.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return nil, err
	}
	return &revision, nil
}

// saveEmailLayoutSource stores document as the source locale's HTML and text
// in the next Content Document revision after expected. A nil document
// changes nothing but still proves the revision and the content fence.
func saveEmailLayoutSource(
	ctx context.Context,
	store Store,
	layoutID string,
	authority LayoutAuthority,
	expected uuid.UUID,
	document *emailutil.LayoutTranslationDocument,
	now time.Time,
) (contentblock.AdvanceResult, error) {
	return store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: authority.DocumentID, ExpectedRevision: expected},
		emailLayoutContentFence(store, layoutID),
		func(ctx context.Context) (contentblock.MetadataEffect, error) {
			if document == nil {
				return contentblock.MetadataEffect{}, nil
			}
			if err := store.SaveLayoutSource(ctx, layoutID, authority.SourceLocale, *document, now); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true, SourceLocale: authority.SourceLocale,
				ChangedLocales: []string{authority.SourceLocale},
			}, nil
		},
	)
}

// saveEmailLayoutTarget stores values over the source HTML as the target
// locale's row, after the row's previous updated_at, and returns the stored
// updated_at.
func saveEmailLayoutTarget(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
	sourceHTML string,
	values map[string]string,
	now time.Time,
	previous time.Time,
) (time.Time, error) {
	contentHTML, contentText, err := emailutil.ApplyLayoutLocaleValues(sourceHTML, values)
	if err != nil {
		return time.Time{}, domainerr.InvalidField("content", err.Error())
	}
	updatedAt := translation.NextTargetUpdatedAt(now, previous)
	return updatedAt, store.UpsertLayoutTranslation(
		ctx, layoutID, locale,
		translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
	)
}

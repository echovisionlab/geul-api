package label

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

// loadLabelSourceLocale reads the Label's source locale; a missing Label
// answers not found.
func loadLabelSourceLocale(ctx context.Context, store Store, labelID string, lock RowLock) (string, error) {
	locale, err := store.SourceLocale(ctx, labelID, lock)
	if errors.Is(err, ErrLabelNotFound) {
		return "", domainerr.ResourceNotFound("label", labelID)
	}
	return locale, err
}

// loadLabelContentSnapshot share-locks the Label's document reference and
// source locale and reads its content document.
func loadLabelContentSnapshot(ctx context.Context, store Store, labelID string) (contentblock.Snapshot, string, error) {
	documentID, err := store.ContentDocumentID(ctx, labelID, ShareLock)
	if err != nil {
		return contentblock.Snapshot{}, "", err
	}
	if documentID == nil || *documentID == uuid.Nil {
		return contentblock.Snapshot{}, "", domainerr.Precondition("label content document is not initialized")
	}
	sourceLocale, err := loadLabelSourceLocale(ctx, store, labelID, ShareLock)
	if err != nil {
		return contentblock.Snapshot{}, "", err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, *documentID, sourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, "", normalizeLabelContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != sourceLocale {
		return contentblock.Snapshot{}, "", errors.New("label content document source locale does not match label source locale")
	}
	return snapshot, sourceLocale, nil
}

type labelContentBootstrap struct {
	Snapshot     contentblock.Snapshot
	SourceLocale string
}

// loadLabelContentBlockBootstrap resolves the collaboration session's
// principal, requires Edit, and reads the Label's document and loadMetadata
// in one snapshot.
func (s *Service) loadLabelContentBlockBootstrap(ctx context.Context, labelID string, sessionID string, loadMetadata func(context.Context, Store) error) (labelContentBootstrap, error) {
	if sessionID == "" {
		return labelContentBootstrap{}, errAuthenticationRequired
	}
	var output labelContentBootstrap
	err := s.units.ReadLocked(ctx, func(store Store) error {
		principal, err := store.ResolveSessionPrincipal(ctx, sessionID)
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return errAuthenticationRequired
		}
		if err != nil {
			return fmt.Errorf("resolve Label collaboration principal: %w", err)
		}
		if principal == nil || !principal.Authenticated {
			return errAuthenticationRequired
		}
		if principal.Banned {
			return domainerr.Denied("account is banned")
		}
		if !principal.Onboarded {
			return domainerr.Denied("no permission to edit this label")
		}
		if err := requireLabelCollaborationEdit(ctx, s.spiceDB, labelID, principal); err != nil {
			return err
		}
		snapshot, sourceLocale, err := loadLabelContentSnapshot(ctx, store, labelID)
		if err != nil {
			return err
		}
		if err := loadMetadata(ctx, store); err != nil {
			return err
		}
		output = labelContentBootstrap{Snapshot: snapshot, SourceLocale: sourceLocale}
		return nil
	})
	return output, err
}

type labelManageContentProjection struct {
	Document    *contentv1.RichTextDocument
	Revision    string
	SourceTitle string
}

// loadCreativeManageContentProjection reads the Label's source-locale
// document and title in one snapshot, after loadRoot reads the root row.
func (s *Service) loadCreativeManageContentProjection(ctx context.Context, labelID string, loadRoot func(context.Context, Store) error) (labelManageContentProjection, error) {
	var projection labelManageContentProjection
	err := s.units.ReadLocked(ctx, func(store Store) error {
		if err := loadRoot(ctx, store); err != nil {
			return err
		}
		snapshot, sourceLocale, err := loadLabelContentSnapshot(ctx, store, labelID)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeLabelContentBlockError(err)
		}
		title, err := store.LocaleTitleText(ctx, labelID, sourceLocale)
		if err != nil {
			if errors.Is(err, ErrLocaleNotFound) {
				return domainerr.ResourceNotFound("label_translation", labelID)
			}
			return err
		}
		projection = labelManageContentProjection{
			Document: document, Revision: snapshot.Document.Revision.String(),
			SourceTitle: title,
		}
		return nil
	})
	return projection, err
}

// initializeCreativeContentDocument replaces the new Label's empty content
// document with input, when the create request carries one.
func initializeCreativeContentDocument(
	ctx context.Context,
	store Store,
	labelID string,
	sourceLocale string,
	created contentblock.Snapshot,
	input *contentv1.RichTextDocument,
) (contentblock.Result, error) {
	if input == nil {
		return contentblock.Result{}, nil
	}
	if input.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT {
		return contentblock.Result{}, domainerr.InvalidField("document.profile", "must be compact")
	}
	if strings.TrimSpace(input.GetSourceLocale()) != sourceLocale {
		return contentblock.Result{}, domainerr.InvalidField("document.source_locale", "must match the server-selected source locale")
	}
	replace, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, input)
	if err != nil {
		return contentblock.Result{}, normalizeLabelContentBlockError(err)
	}
	result, err := store.ReplaceContentSnapshot(ctx, replace, labelContentCreationFence(labelID, sourceLocale))
	if err != nil {
		return contentblock.Result{}, normalizeLabelContentBlockError(err)
	}
	return result, nil
}

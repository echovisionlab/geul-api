package public

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// loadHistory reads an active or archived version by ID; any other
// version is absent.
func (s *Service) loadHistory(ctx context.Context, kind publicKind, query GetPolicyQuery) (PublicPolicies, error) {
	var row Version
	var found bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		row, found, err = store.PublishedVersion(ctx, kind.kind, query.ID)
		return err
	}); err != nil || !found {
		return PublicPolicies{}, err
	}
	projected, err := s.project(ctx, kind, row, query.AcceptLanguage)
	return PublicPolicies{Current: projected}, err
}

// loadShared reads the version a valid Share Link opens: a published
// version as the current one, a version scheduled for later as the
// scheduled one. Anything else is absent.
func (s *Service) loadShared(ctx context.Context, kind publicKind, query GetPolicyQuery) (PublicPolicies, error) {
	if query.ID == "" {
		return PublicPolicies{}, nil
	}
	access, valid, err := s.resolveLegalShareLinkAccess(ctx, kind, query.ShareToken, query.SharePassword, query.ID, query.Now)
	if err != nil || !valid {
		return PublicPolicies{}, err
	}
	var row Version
	var found bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		row, found, err = store.VersionInStatus(ctx, kind.kind, query.ID, access.status)
		return err
	}); err != nil || !found {
		return PublicPolicies{}, err
	}
	projected, err := s.project(ctx, kind, row, query.AcceptLanguage)
	if err != nil {
		return PublicPolicies{}, err
	}
	if access.publicHistory {
		return PublicPolicies{Current: projected}, nil
	}
	return PublicPolicies{Scheduled: projected}, nil
}

// loadCurrent reads the effective active version and announces the next
// scheduled one. A failed scheduled lookup serves the current version
// alone.
func (s *Service) loadCurrent(ctx context.Context, kind publicKind, query GetPolicyQuery) (PublicPolicies, error) {
	result := PublicPolicies{}
	var active Version
	var found bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		active, found, err = store.EffectiveVersion(ctx, kind.kind, query.Now)
		return err
	}); err != nil {
		return result, err
	}
	if found {
		var err error
		if result.Current, err = s.project(ctx, kind, active, query.AcceptLanguage); err != nil {
			return PublicPolicies{}, err
		}
	}

	var scheduled Version
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		scheduled, found, err = store.NextScheduledVersion(ctx, kind.kind, query.Now)
		return err
	})
	if err == nil && found {
		result.Scheduled = &PublicPolicy{
			ID: scheduled.ID, Version: int32(scheduled.Version),
			Title:         resolveCanonicalLegalPublicTitle(scheduled.Title, nil),
			EffectiveFrom: scheduled.EffectiveFrom, EffectiveUntil: scheduled.EffectiveUntil,
		}
	} else if err != nil {
		slog.ErrorContext(ctx, "Scheduled legal document lookup failed; serving the current version only", "domain", "legal", "operation", "load_scheduled_document", "outcome", "failed", "entity_type", kind.entity, failurecause.Attr(err))
	}
	return result, nil
}

// project localizes row for acceptLanguage, keeping the displayed locale
// consistent with its OG image, and loads its document in that locale.
func (s *Service) project(ctx context.Context, kind publicKind, row Version, acceptLanguage string) (*PublicPolicy, error) {
	localization, err := s.localizer.Resolve(ctx, kind.kind, row.ID, acceptLanguage)
	if err != nil {
		return nil, err
	}
	localization, ogAsset, err := s.resolveOGConsistency(ctx, kind, row.ID, localization)
	if err != nil {
		return nil, err
	}
	content, err := s.loadContentProjection(ctx, kind, row.ID, &localization)
	if err != nil {
		return nil, err
	}
	return &PublicPolicy{
		ID: row.ID, Version: int32(row.Version),
		Title:    resolveCanonicalLegalPublicTitle(row.Title, &localization),
		Document: content.document, Revision: content.revision,
		EffectiveFrom: row.EffectiveFrom, EffectiveUntil: row.EffectiveUntil,
		Localization: &localization, OGAsset: ogAsset,
	}, nil
}

// resolveOGConsistency serves the displayed locale only once its OG image
// is ready; a locale whose image failed falls back to the source locale.
func (s *Service) resolveOGConsistency(
	ctx context.Context,
	kind publicKind,
	entityID string,
	selection publiccontent.Selection,
) (publiccontent.Selection, *Asset, error) {
	loadExactAsset := func(locale string) (*Asset, error) {
		return s.ogImages.ReadyLocalizedOGImage(ctx, kind.kind, locale)
	}
	if selection.DisplayedLocale == selection.SourceLocale {
		asset, err := loadExactAsset(selection.SourceLocale)
		return selection, asset, err
	}
	disposition, err := s.ogImages.LocalizedOGDisposition(ctx, kind.kind, selection.DisplayedLocale)
	if err != nil {
		return selection, nil, err
	}
	switch disposition {
	case OGPending:
		return selection, nil, nil
	case OGReady:
		asset, err := loadExactAsset(selection.DisplayedLocale)
		if err != nil {
			return selection, nil, err
		}
		if asset != nil {
			return selection, asset, nil
		}
	}
	fallback, err := s.localizer.FallbackToSource(ctx, kind.kind, entityID, selection)
	if err != nil {
		return selection, nil, err
	}
	asset, err := loadExactAsset(fallback.SourceLocale)
	return fallback, asset, err
}

type publicLegalContentProjection struct {
	document *contentv1.LocalizedRichTextDocument
	revision string
}

// loadContentProjection loads the version's document in the selected
// locale. A version deleted after it was selected answers NotFound.
func (s *Service) loadContentProjection(
	ctx context.Context,
	kind publicKind,
	entityID string,
	selection *publiccontent.Selection,
) (publicLegalContentProjection, error) {
	var root DocumentRoot
	var snapshot contentblock.Snapshot
	err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		if root, err = store.DocumentRoot(ctx, kind.kind, entityID); err != nil {
			if errors.Is(err, legaldomain.ErrPolicyNotFound) {
				return domainerr.ResourceNotFound(kind.entity, entityID)
			}
			return err
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
			return domainerr.Precondition(kind.entity + " content document has not been populated")
		}
		if strings.TrimSpace(root.SourceLocale) == "" {
			return domainerr.Precondition(kind.entity + " source locale is missing")
		}
		// A version's document and source locale never change.
		snapshot, err = store.ContentSnapshot(ctx, *root.ContentDocumentID, root.SourceLocale)
		if errors.Is(err, contentblock.ErrDocumentNotFound) {
			return domainerr.ResourceNotFound(kind.entity, entityID).WithCause(err)
		}
		return err
	})
	if err != nil {
		return publicLegalContentProjection{}, err
	}
	if snapshot.SourceLocale != root.SourceLocale {
		return publicLegalContentProjection{}, domainerr.Precondition(kind.entity + " source locale does not match its content document")
	}
	locale := root.SourceLocale
	if selection != nil && strings.TrimSpace(selection.DisplayedLocale) != "" {
		locale = selection.DisplayedLocale
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return publicLegalContentProjection{}, err
	}
	return publicLegalContentProjection{document: document, revision: snapshot.Document.Revision.String()}, nil
}

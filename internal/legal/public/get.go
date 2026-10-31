package public

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// loadHistory reads an active or archived version by ID; any other
// version is absent.
func (s *Service) loadHistory(ctx context.Context, kind publicKind, query GetPolicyQuery) (PublicPolicies, error) {
	var row publicPolicyRow
	err := s.db.WithContext(ctx).Table(kind.table).
		Where("id = ?", query.ID).
		Where("status IN ?", []model.LegalPolicyStatus{kind.active, kind.archived}).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PublicPolicies{}, nil
	}
	if err != nil {
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
	access, valid, err := resolveLegalShareLinkAccess(ctx, s.db, kind, query.ShareToken, query.SharePassword, query.ID, query.Now)
	if err != nil || !valid {
		return PublicPolicies{}, err
	}
	var row publicPolicyRow
	err = s.db.WithContext(ctx).Table(kind.table).
		Where("id = ? AND status = ?", query.ID, access.status).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PublicPolicies{}, nil
	}
	if err != nil {
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
	var active publicPolicyRow
	err := s.db.WithContext(ctx).Table(kind.table).
		Where("status = ?", kind.active).
		Where("effective_from <= ?", query.Now).
		Where("effective_until IS NULL OR effective_until > ?", query.Now).
		Order("version DESC").
		First(&active).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return result, err
	}
	if err == nil {
		if result.Current, err = s.project(ctx, kind, active, query.AcceptLanguage); err != nil {
			return PublicPolicies{}, err
		}
	}

	var scheduled publicPolicyRow
	err = s.db.WithContext(ctx).Table(kind.table).
		Where("status = ?", kind.scheduled).
		Where("effective_from > ?", query.Now).
		Order("effective_from ASC").
		First(&scheduled).Error
	if err == nil {
		result.Scheduled = &PublicPolicy{
			ID: scheduled.ID, Version: int32(scheduled.Version),
			Title:         resolveCanonicalLegalPublicTitle(scheduled.Title, nil),
			EffectiveFrom: scheduled.EffectiveFrom, EffectiveUntil: scheduled.EffectiveUntil,
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.ErrorContext(ctx, "Scheduled legal document lookup failed; serving the current version only", "domain", "legal", "operation", "load_scheduled_document", "outcome", "failed", "entity_type", kind.entity, failurecause.Attr(err))
	}
	return result, nil
}

// project localizes row for acceptLanguage, keeping the displayed locale
// consistent with its OG image, and loads its document in that locale.
func (s *Service) project(ctx context.Context, kind publicKind, row publicPolicyRow, acceptLanguage string) (*PublicPolicy, error) {
	localization, err := publiccontent.Resolve(ctx, s.db, kind.localization, row.ID, acceptLanguage)
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
) (publiccontent.Selection, *commonv1.AssetRef, error) {
	routeID := s.media.RouteID(kind.entity)
	loadExactAsset := func(locale string) (*commonv1.AssetRef, error) {
		return s.media.ReadyLocalizedOGAsset(ctx, s.db, kind.entity, routeID, locale)
	}
	if selection.DisplayedLocale == selection.SourceLocale {
		asset, err := loadExactAsset(selection.SourceLocale)
		return selection, asset, err
	}
	disposition, err := s.media.LocalizedOGDisposition(ctx, s.db, kind.entity, routeID, selection.DisplayedLocale)
	if err != nil {
		return selection, nil, err
	}
	switch disposition {
	case legaldomain.OGPending:
		return selection, nil, nil
	case legaldomain.OGReady:
		asset, err := loadExactAsset(selection.DisplayedLocale)
		if err != nil {
			return selection, nil, err
		}
		if asset != nil {
			return selection, asset, nil
		}
	}
	fallback, err := publiccontent.FallbackToSource(ctx, s.db, kind.localization, entityID, selection)
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
	var root struct {
		ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
		SourceLocale      string     `gorm:"column:source_locale"`
	}
	if err := s.db.WithContext(ctx).Table(kind.table).
		Select("content_document_id", "source_locale").
		Where("id = ?", entityID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return publicLegalContentProjection{}, domainerr.ResourceNotFound(kind.entity, entityID)
		}
		return publicLegalContentProjection{}, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return publicLegalContentProjection{}, domainerr.Precondition(kind.entity + " content document has not been populated")
	}
	if strings.TrimSpace(root.SourceLocale) == "" {
		return publicLegalContentProjection{}, domainerr.Precondition(kind.entity + " source locale is missing")
	}
	// A version's document and source locale never change, and the
	// snapshot reads in its own repeatable-read transaction.
	snapshot, err := s.contentBlocks.LoadSnapshot(ctx, s.db, *root.ContentDocumentID, root.SourceLocale)
	if errors.Is(err, contentblock.ErrDocumentNotFound) {
		return publicLegalContentProjection{}, domainerr.ResourceNotFound(kind.entity, entityID).WithCause(err)
	}
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

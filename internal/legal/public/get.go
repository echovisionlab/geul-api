package public

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type publicLegalSpec[Document structured.Value, Proto structured.Value] struct {
	entityName      string
	shareEntityType managev1.ShareLinkEntityType
	activeStatus    string
	archivedStatus  string
	scheduledStatus string
	id              func(*Document) string
	title           func(*Document) string
	setTitle        func(*Document, string)
	toProto         func(*Document, *publiccontent.Selection, *commonv1.AssetRef, publicLegalContentProjection) *Proto
	toScheduled     func(*Document) *Proto
}

type publicLegalContentProjection struct {
	document *contentv1.LocalizedRichTextDocument
	revision string
}

type publicLegalResult[Proto interface{}] struct {
	current   *Proto
	scheduled *Proto
}

type publicLegalRequest struct {
	requestedID    string
	shareToken     string
	sharePassword  string
	acceptLanguage string
	now            time.Time
}

func getPublicLegalDocument[Document structured.Value, Proto structured.Value](
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
	spec publicLegalSpec[Document, Proto],
	request publicLegalRequest,
) (publicLegalResult[Proto], error) {
	if request.requestedID != "" && request.shareToken == "" {
		return loadPublicLegalHistory(ctx, db, contentBlocks, media, spec, request)
	}
	if request.shareToken != "" {
		return loadSharedPublicLegalDocument(ctx, db, contentBlocks, media, spec, request)
	}
	return loadCurrentPublicLegalDocuments(ctx, db, contentBlocks, media, spec, request)
}

func loadPublicLegalHistory[Document structured.Value, Proto structured.Value](
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
	spec publicLegalSpec[Document, Proto],
	request publicLegalRequest,
) (publicLegalResult[Proto], error) {
	var document Document
	err := db.WithContext(ctx).
		Where("id = ?", request.requestedID).
		Where("status IN ?", []string{spec.activeStatus, spec.archivedStatus}).
		First(&document).Error
	if err == gorm.ErrRecordNotFound {
		return publicLegalResult[Proto]{}, nil
	}
	if err != nil {
		return publicLegalResult[Proto]{}, errs.Internal(err)
	}
	projected, err := projectPublicLegalDocument(ctx, db, contentBlocks, media, spec, &document, request.acceptLanguage)
	return publicLegalResult[Proto]{current: projected}, err
}

func loadSharedPublicLegalDocument[Document structured.Value, Proto structured.Value](
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
	spec publicLegalSpec[Document, Proto],
	request publicLegalRequest,
) (publicLegalResult[Proto], error) {
	if request.requestedID == "" {
		return publicLegalResult[Proto]{}, nil
	}
	access, valid, err := resolveLegalShareLinkAccess(
		ctx, db, request.shareToken, request.sharePassword,
		spec.shareEntityType, request.requestedID, request.now,
	)
	if err != nil {
		return publicLegalResult[Proto]{}, errs.Internal(err)
	}
	if !valid {
		return publicLegalResult[Proto]{}, nil
	}
	var document Document
	err = db.WithContext(ctx).
		Where("id = ? AND status = ?", request.requestedID, access.status).
		First(&document).Error
	if err == gorm.ErrRecordNotFound {
		return publicLegalResult[Proto]{}, nil
	}
	if err != nil {
		return publicLegalResult[Proto]{}, errs.Internal(err)
	}
	projected, err := projectPublicLegalDocument(ctx, db, contentBlocks, media, spec, &document, request.acceptLanguage)
	if err != nil {
		return publicLegalResult[Proto]{}, err
	}
	if access.publicHistory {
		return publicLegalResult[Proto]{current: projected}, nil
	}
	return publicLegalResult[Proto]{scheduled: projected}, nil
}

func loadCurrentPublicLegalDocuments[Document structured.Value, Proto structured.Value](
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
	spec publicLegalSpec[Document, Proto],
	request publicLegalRequest,
) (publicLegalResult[Proto], error) {
	result := publicLegalResult[Proto]{}
	var active Document
	err := db.WithContext(ctx).
		Where("status = ?", spec.activeStatus).
		Where("effective_from <= ?", request.now).
		Where("effective_until IS NULL OR effective_until > ?", request.now).
		Order("version DESC").
		First(&active).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return result, errs.Internal(err)
	}
	if err == nil {
		result.current, err = projectPublicLegalDocument(ctx, db, contentBlocks, media, spec, &active, request.acceptLanguage)
		if err != nil {
			return publicLegalResult[Proto]{}, err
		}
	}

	var scheduled Document
	err = db.WithContext(ctx).
		Where("status = ?", spec.scheduledStatus).
		Where("effective_from > ?", request.now).
		Order("effective_from ASC").
		First(&scheduled).Error
	if err == nil {
		result.scheduled = spec.toScheduled(&scheduled)
	}
	return result, nil
}

func projectPublicLegalDocument[Document structured.Value, Proto structured.Value](
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
	spec publicLegalSpec[Document, Proto],
	document *Document,
	acceptLanguage string,
) (*Proto, error) {
	localization, selectionErr := publiccontent.Resolve(
		ctx, db, legalLocalizationSpec(spec.entityName), spec.id(document), acceptLanguage,
	)
	if selectionErr != nil {
		return nil, selectionErr
	}
	localization, ogAsset, err := resolvePublicLegalOgConsistency(
		ctx,
		db,
		media,
		spec.entityName,
		spec.id(document),
		localization,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	selection := &localization
	spec.setTitle(document, resolveCanonicalLegalPublicTitle(spec.title(document), selection))
	content, err := loadPublicLegalContentProjection(
		ctx, db, contentBlocks, spec.entityName, spec.id(document), selection,
	)
	if err != nil {
		return nil, err
	}
	return spec.toProto(document, selection, ogAsset, content), nil
}

func resolvePublicLegalOgConsistency(
	ctx context.Context,
	db *gorm.DB,
	media legaldomain.PublicMedia,
	entityType string,
	entityID string,
	selection publiccontent.Selection,
) (publiccontent.Selection, *commonv1.AssetRef, error) {
	if media == nil {
		return selection, nil, errs.InternalMsg(entityType + " public media is not configured")
	}
	routeID := media.RouteID(entityType)
	loadExactAsset := func(locale string) (*commonv1.AssetRef, error) {
		return media.ReadyLocalizedOGAsset(ctx, db, entityType, routeID, locale)
	}
	if selection.DisplayedLocale == selection.SourceLocale {
		asset, err := loadExactAsset(selection.SourceLocale)
		return selection, asset, err
	}
	disposition, err := media.LocalizedOGDisposition(ctx, db, entityType, routeID, selection.DisplayedLocale)
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
	fallback, err := publiccontent.FallbackToSource(
		ctx, db, legalLocalizationSpec(entityType), entityID, selection,
	)
	if err != nil {
		return selection, nil, err
	}
	asset, err := loadExactAsset(fallback.SourceLocale)
	return fallback, asset, err
}

func loadPublicLegalContentProjection(
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	entityType string,
	entityID string,
	selection *publiccontent.Selection,
) (publicLegalContentProjection, error) {
	if contentBlocks == nil {
		return publicLegalContentProjection{}, errs.InternalMsg(entityType + " content Block store is not configured")
	}
	var snapshot contentblock.Snapshot
	var source struct {
		Locale string `gorm:"column:source_locale"`
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root struct {
			ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
			SourceLocale      string     `gorm:"column:source_locale"`
		}
		if err := tx.Table(entityType+"_history").
			Clauses(clause.Locking{Strength: "SHARE"}).
			Select("content_document_id", "source_locale").
			Where("id = ?", entityID).
			Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound(entityType, entityID)
			}
			return errs.Internal(err)
		}
		if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
			return errs.FailedPrecondition(entityType + " content document has not been populated")
		}
		source.Locale = root.SourceLocale
		if strings.TrimSpace(source.Locale) == "" {
			return errs.FailedPrecondition(entityType + " source locale is missing")
		}
		var loadErr error
		snapshot, loadErr = contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, *root.ContentDocumentID, source.Locale,
		)
		if loadErr == nil && snapshot.SourceLocale != source.Locale {
			return errs.FailedPrecondition(entityType + " source locale does not match its content document")
		}
		return loadErr
	})
	if err != nil {
		return publicLegalContentProjection{}, errs.Wrap(err)
	}
	locale := source.Locale
	if selection != nil && strings.TrimSpace(selection.DisplayedLocale) != "" {
		locale = selection.DisplayedLocale
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return publicLegalContentProjection{}, errs.Internal(err)
	}
	return publicLegalContentProjection{
		document: document,
		revision: snapshot.Document.Revision.String(),
	}, nil
}

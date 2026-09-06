package label

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type InternalLabelService struct {
	db             *gorm.DB
	asyncPublisher AsyncPublisher
	spiceDB        *auth.SpiceDBClient
	checkpoints    persistencecheckpoint.ContributorFence
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	runtime        Runtime
	translation    Translation
}

type InternalLabelServiceOption func(*InternalLabelService)

func WithInternalLabelContentBlockStore(store *contentblock.Store) InternalLabelServiceOption {
	return func(service *InternalLabelService) { service.contentBlocks = store }
}

func WithInternalLabelCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalLabelServiceOption {
	return func(service *InternalLabelService) { service.checkpoints = checkpoints }
}

func NewAuditedInternalLabelService(db *gorm.DB, asyncPublisher AsyncPublisher, spiceDB *auth.SpiceDBClient, auditWriter domainaudit.Appender, dependencies Dependencies, options ...InternalLabelServiceOption) *InternalLabelService {
	if auditWriter == nil {
		panic("internal label audit writer is required")
	}
	service := NewInternalLabelService(db, asyncPublisher, spiceDB, dependencies, options...)
	service.auditWriter = auditWriter
	return service
}

func NewInternalLabelService(db *gorm.DB, asyncPublisher AsyncPublisher, spiceDB *auth.SpiceDBClient, dependencies Dependencies, options ...InternalLabelServiceOption) *InternalLabelService {
	if db == nil {
		panic("InternalLabelService: db is required")
	}
	if asyncPublisher == nil {
		panic("InternalLabelService: asyncPublisher is required")
	}
	if spiceDB == nil {
		panic("InternalLabelService: spiceDB is required")
	}
	if dependencies.Translation == nil {
		panic("InternalLabelService: translation is required")
	}
	if dependencies.Runtime == nil {
		panic("InternalLabelService: media and OG runtime is required")
	}
	service := &InternalLabelService{
		db: db, asyncPublisher: asyncPublisher, spiceDB: spiceDB,
		runtime: dependencies.Runtime, translation: dependencies.Translation,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func validateLabelParentTransition(ctx context.Context, tx *gorm.DB, labelID, parentID string) error {
	if err := tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtext('label_parent_graph'))").Error; err != nil {
		return err
	}
	if parentID == "" {
		return nil
	}
	if parentID == labelID {
		return errs.InvalidArgument("parent_label_id", "cannot reference the same label")
	}
	if !IsValidUUID(parentID) {
		return errs.InvalidArgument("parent_label_id", "must be a valid Label ID")
	}
	var parentExists bool
	if err := tx.WithContext(ctx).
		Raw(`SELECT EXISTS(SELECT 1 FROM label WHERE id = ?::uuid)`, parentID).
		Scan(&parentExists).Error; err != nil {
		return err
	}
	if !parentExists {
		return errs.InvalidArgument("parent_label_id", "label does not exist")
	}
	var cycle bool
	if err := tx.WithContext(ctx).Raw(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM label WHERE parent_label_id = ?
			UNION
			SELECT child.id
			FROM label child
			JOIN descendants parent ON child.parent_label_id = parent.id
		)
		SELECT EXISTS(SELECT 1 FROM descendants WHERE id = ?)
	`, labelID, parentID).Scan(&cycle).Error; err != nil {
		return err
	}
	if cycle {
		return errs.InvalidArgument("parent_label_id", "would create a label hierarchy cycle")
	}
	return nil
}

func (s *InternalLabelService) LoadLabelBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadLabelBlockDocumentRequest],
) (*connect.Response[intrav1.LoadLabelBlockDocumentResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetLocale()) == "" {
		return nil, errs.Required("locale")
	}
	requestedLocale, err := normalizeLabelDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	var locales []labelLocaleTitle
	var metadata *intrav1.LabelDocumentMetadata
	bootstrap, err := loadLabelContentBlockBootstrap(
		ctx, s.db, s.contentBlocks, s.spiceDB, req.Msg.LabelId,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_LABEL,
		req.Msg.Principal,
		func(ctx context.Context, tx *gorm.DB) error {
			var err error
			locales, err = loadLabelLocaleTitles(ctx, tx, req.Msg.LabelId)
			if err != nil {
				return err
			}
			metadata, err = loadLabelDocumentMetadata(ctx, tx, req.Msg.LabelId)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	// Collaboration rooms render the current source graph with sparse target
	// values overlaid. Missing target units must therefore remain visible via
	// the source fallback; the sparse projection is reserved for exact
	// translation/DCDP presence reads.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(bootstrap.Snapshot, requestedLocale)
	if err != nil {
		return nil, normalizeLabelContentBlockError(err)
	}
	sourceMetadata := &intrav1.LabelLocaleMetadata{Locale: bootstrap.Source.SourceLocale}
	var localeMetadata *intrav1.LabelLocaleMetadata
	localeExists := false
	var targetRevision *string
	for _, locale := range locales {
		if locale.Locale == bootstrap.Source.SourceLocale {
			sourceMetadata.Title = locale.Title
		}
		if locale.Locale == requestedLocale {
			localeExists = true
			localeMetadata = &intrav1.LabelLocaleMetadata{Locale: requestedLocale}
			localeMetadata.Title = locale.Title
			if requestedLocale != bootstrap.Source.SourceLocale {
				revision, revisionErr := deriveLabelTargetRevision(bootstrap.Snapshot.Document.Revision.String(), locale.UpdatedAt)
				if revisionErr != nil {
					return nil, revisionErr
				}
				targetRevision = &revision
			}
		}
	}
	if requestedLocale == bootstrap.Source.SourceLocale && !localeExists {
		return nil, errs.InternalMsg("Label source locale metadata is missing")
	}
	if !localeExists && labelSnapshotContainsLocale(bootstrap.Snapshot, requestedLocale) {
		return nil, errs.FailedPrecondition("Label target locale Blocks exist without owning metadata")
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, requestedLocale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.LoadLabelBlockDocumentResponse{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: sourceMetadata, Metadata: metadata,
		Locale: requestedLocale, LocaleExists: localeExists, LocaleMetadata: localeMetadata,
		TargetRevision: targetRevision, PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalLabelService) ApplyLabelBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyLabelBlockBatchRequest],
) (*connect.Response[intrav1.ApplyLabelBlockBatchResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	locale, err := normalizeLabelDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	result, err := s.applyLabelBlockBatch(ctx, req.Msg.LabelId, locale, req.Msg.ExpectedTargetRevision, req.Msg.Batch, req.Msg.AffectedLocaleValues)
	if err != nil {
		return nil, err
	}
	if result.Content.Changed {
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelContentUpdatedEvent(
			req.Msg.LabelId,
			[]string{"content"},
			result.Content.DocumentRevision.String(),
			req.Msg.Batch.GetContributorMemberIds(),
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			result.LocaleExists,
			result.TargetRevision,
			locale == result.SourceLocale,
		))
	}
	return connect.NewResponse(&intrav1.ApplyLabelBlockBatchResponse{
		DocumentRevision: result.Content.DocumentRevision.String(), Changed: result.Content.Changed,
		SourceChanged:  result.Content.TranslationSourceChanged,
		ChangedLocales: result.Content.ChangedLocales,
		Locale:         locale, TargetRevision: result.TargetRevision,
	}), nil
}

func (s *InternalLabelService) UpdateLabelLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateLabelLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateLabelLocaleMetadataResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	locale, err := normalizeLabelDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	result, err := s.updateLabelLocaleTitle(ctx, req.Msg.LabelId, locale, req.Msg.Title, req.Msg.ExpectedRevision, req.Msg.ExpectedTargetRevision, req.Msg.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	if result.Advance.Changed {
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelContentUpdatedEvent(
			req.Msg.LabelId,
			[]string{"title"},
			result.Advance.DocumentRevision.String(),
			req.Msg.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			nil,
			true,
		))
	}
	return connect.NewResponse(&intrav1.UpdateLabelLocaleMetadataResponse{
		DocumentRevision: result.Advance.DocumentRevision.String(), Changed: result.Advance.Changed,
		SourceChanged:  result.Advance.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales,
		Locale:         locale,
	}), nil
}

func loadLabelDocumentMetadata(
	ctx context.Context,
	db *gorm.DB,
	labelID string,
) (*intrav1.LabelDocumentMetadata, error) {
	var label model.Label
	if err := db.WithContext(ctx).
		Select("id", "slug", "country_code", "website", "social_links", "parent_label_id").
		Where("id = ?", labelID).
		Take(&label).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("label", labelID)
		}
		return nil, errs.Internal(err)
	}
	return &intrav1.LabelDocumentMetadata{
		Slug:          label.Slug,
		CountryCode:   label.CountryCode,
		Website:       label.Website,
		SocialLinks:   label.SocialLinks,
		ParentLabelId: label.ParentLabelID,
	}, nil
}

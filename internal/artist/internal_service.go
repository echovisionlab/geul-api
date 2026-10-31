package artist

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LoadArtistBlockDocumentQuery opens the Artist document of a collaboration
// room in Locale for the session SessionID.
type LoadArtistBlockDocumentQuery struct {
	ArtistID  string
	Locale    string
	SessionID string
}

// LoadArtistBlockDocument returns the room's document with its locale and
// Artist metadata.
func (s *Service) LoadArtistBlockDocument(ctx context.Context, query LoadArtistBlockDocumentQuery) (*intrav1.LoadArtistBlockDocumentResponse, error) {
	locale, err := normalizeArtistDocumentLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	var localeState artistTargetLocaleState
	var metadata *intrav1.ArtistDocumentMetadata
	bootstrap, err := loadCreativeContentBlockBootstrap(
		ctx, s.db, s.contentBlocks, s.spiceDB, artistContentEntity, query.ArtistID, query.SessionID,
		func(ctx context.Context, tx *gorm.DB) error {
			documentID, err := loadCreativeContentDocumentID(ctx, tx, query.ArtistID)
			if err != nil {
				return err
			}
			if localeState, err = loadArtistTargetLocaleState(ctx, tx, s.contentBlocks, query.ArtistID, documentID, locale, false); err != nil {
				return err
			}
			metadata, err = loadArtistDocumentMetadata(ctx, tx, query.ArtistID)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	document, err := artistLocalizedDocument(localeState, locale)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, locale)
	if err != nil {
		return nil, err
	}
	response := &intrav1.LoadArtistBlockDocumentResponse{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.ArtistLocaleMetadata{Locale: localeState.SourceLocale, Title: localeState.SourceMetadata.Title},
		Metadata:       metadata, Locale: locale,
		LocaleExists: localeState.TargetMetadata != nil, PresentLocaleValues: presentLocaleValues,
	}
	if localeState.TargetMetadata != nil {
		response.LocaleMetadata = &intrav1.ArtistLocaleMetadata{Locale: locale, Title: localeState.TargetMetadata.Title}
		if locale != localeState.SourceLocale {
			response.TargetRevision = &localeState.TargetRevision
		}
	}
	return response, nil
}

// ApplyArtistBlockBatchCommand applies a collaboration room's Block batch
// to the Artist document in Locale.
type ApplyArtistBlockBatchCommand struct {
	ArtistID               string
	Locale                 string
	ExpectedTargetRevision *string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// BlockBatchResult is an applied collaboration batch. TargetRevision is
// set for a target-locale room.
type BlockBatchResult struct {
	Locale         string
	SourceLocale   string
	Content        contentblock.Result
	TargetRevision *string
}

// artistRoomBatch is a collaboration batch under the locked Artist row.
type artistRoomBatch struct {
	artistID               string
	locale                 string
	documentID             uuid.UUID
	domain                 contentblock.DomainContext
	expectedTargetRevision *string
	input                  *contentv1.RichTextBlockMutationBatch
	storage                contentv1.ContentStorageMutationBatch
	contributor            string
}

// ApplyArtistBlockBatch applies the batch on behalf of its contributors,
// whose current collaboration authority is rechecked under the Artist lock.
// Every failure gets the content document's client answer.
func (s *Service) ApplyArtistBlockBatch(ctx context.Context, command ApplyArtistBlockBatchCommand) (BlockBatchResult, error) {
	if command.Batch == nil {
		return BlockBatchResult{}, domainerr.Required("batch")
	}
	batch, err := s.prepareArtistRoomBatch(ctx, command)
	if err != nil {
		return BlockBatchResult{}, err
	}
	contributors := command.Batch.ContributorMemberIds
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[BlockBatchResult]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			batch.domain, err = internalCreativeContentFence(command.ArtistID)(ctx, tx, batch.documentID)
			return err
		},
		Access: s.contributorAccess(command.ArtistID, contributors),
		Change: func(ctx context.Context, tx *gorm.DB) (BlockBatchResult, error) {
			batch.contributor = contributors[0]
			if batch.locale != batch.domain.SourceLocale {
				return s.applyArtistTargetRoomBatch(ctx, tx, batch)
			}
			return s.applyArtistSourceRoomBatch(ctx, tx, batch)
		},
		Audit: func(result BlockBatchResult) mutation.Audit {
			if !result.Content.Changed {
				return mutation.Unchanged()
			}
			return artistMemberLocaleAudit(batch.contributor, command.ArtistID, result.Locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, result BlockBatchResult) error {
			if !result.Content.Changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             command.ArtistID,
				Fields:               []string{"content"},
				DocumentRevision:     result.Content.DocumentRevision.String(),
				Contributors:         contributors,
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               batch.locale,
				TargetRevision:       result.TargetRevision,
				DocumentStateChanged: batch.locale == result.SourceLocale,
			}))
		},
	})
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	return result, nil
}

// prepareArtistRoomBatch validates the batch before its transaction.
func (s *Service) prepareArtistRoomBatch(ctx context.Context, command ApplyArtistBlockBatchCommand) (artistRoomBatch, error) {
	locale, err := normalizeArtistDocumentLocale(command.Locale)
	if err != nil {
		return artistRoomBatch{}, err
	}
	documentID, err := loadCreativeContentDocumentID(ctx, s.db, command.ArtistID)
	if err != nil {
		return artistRoomBatch{}, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(command.Batch, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
	if err != nil {
		return artistRoomBatch{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(command.Batch.Profile, locale, &storage, command.AffectedLocaleValues); err != nil {
		return artistRoomBatch{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	expectedDocumentRevision, err := uuid.Parse(command.Batch.ExpectedRevision)
	if err != nil || expectedDocumentRevision == uuid.Nil {
		return artistRoomBatch{}, domainerr.InvalidField("batch.expected_revision", "must be a UUID")
	}
	return artistRoomBatch{
		artistID: command.ArtistID, locale: locale, documentID: documentID,
		expectedTargetRevision: command.ExpectedTargetRevision, input: command.Batch, storage: storage,
	}, nil
}

// contributorAccess authorizes a collaboration save by its contributors and
// rechecks their current room authority.
func (s *Service) contributorAccess(artistID string, contributors []string) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		if err := s.translation.RequireDocumentContributors(ctx, tx, contributors); err != nil {
			return err
		}
		return requireArtistCollaborationContributors(ctx, tx, s.checkpoints, artistID, contributors)
	})
}

func (s *Service) applyArtistTargetRoomBatch(ctx context.Context, tx *gorm.DB, b artistRoomBatch) (BlockBatchResult, error) {
	batch, err := contentblock.BatchFromRichTextStorage(b.documentID, b.input.Profile, b.storage)
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	result, targetRevision, err := applyArtistTargetLocaleMutation(ctx, tx, s.contentBlocks, artistTargetLocaleMutationInput{
		ArtistID: b.artistID, DocumentID: b.documentID, Locale: b.locale, Batch: batch,
		ExpectedTargetRevision: b.expectedTargetRevision, Now: time.Now().UTC(),
		Fence: internalCreativeContentFence(b.artistID),
	})
	if err != nil {
		return BlockBatchResult{}, err
	}
	return BlockBatchResult{Locale: b.locale, SourceLocale: b.domain.SourceLocale, Content: result, TargetRevision: &targetRevision}, nil
}

func (s *Service) applyArtistSourceRoomBatch(ctx context.Context, tx *gorm.DB, b artistRoomBatch) (BlockBatchResult, error) {
	if b.expectedTargetRevision != nil {
		return BlockBatchResult{}, domainerr.InvalidField("expected_target_revision", "source Artist room cannot carry a target revision")
	}
	if err := validateArtistSourceStorage(b.storage, b.domain.SourceLocale); err != nil {
		return BlockBatchResult{}, err
	}
	batch, err := contentblock.BatchFromRichTextStorage(b.documentID, b.input.Profile, b.storage)
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, artistSourceRoomFence(b.artistID, b.domain.SourceLocale))
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.Artist{}).Where("id = ?", b.artistID).UpdateColumn("updated_at", time.Now().UTC()).Error; err != nil {
			return BlockBatchResult{}, err
		}
	}
	if result.TranslationSourceChanged {
		if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, b.artistID, "", false, "artist_source_document_saved"); err != nil {
			return BlockBatchResult{}, err
		}
	}
	return BlockBatchResult{Locale: b.locale, SourceLocale: b.domain.SourceLocale, Content: result}, nil
}

// UpdateArtistLocaleMetadataCommand saves the title of the Artist's room
// locale. A target locale room names the target revision it read.
type UpdateArtistLocaleMetadataCommand struct {
	ArtistID               string
	Locale                 string
	Title                  *string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	ContributorMemberIDs   []string
}

// TitleUpdate is a saved locale title.
type TitleUpdate struct {
	Locale         string
	Result         contentblock.Result
	SourceChanged  bool
	TargetRevision *string
}

// UpdateArtistLocaleMetadata saves the room locale's title. Collaboration
// title saves have never been audited, so the pipeline records none.
func (s *Service) UpdateArtistLocaleMetadata(ctx context.Context, command UpdateArtistLocaleMetadataCommand) (TitleUpdate, error) {
	locale, err := normalizeArtistDocumentLocale(command.Locale)
	if err != nil {
		return TitleUpdate{}, err
	}
	sourceAuthority, err := loadCreativeDocumentAuthority(ctx, s.db, artistContentEntity, command.ArtistID)
	if err != nil {
		return TitleUpdate{}, err
	}
	title, err := s.prepareArtistTitle(ctx, command, locale, locale != sourceAuthority.SourceLocale)
	if err != nil {
		return TitleUpdate{}, err
	}
	return mutation.Run(ctx, s.mutations, mutation.Spec[TitleUpdate]{
		Access: s.contributorAccess(command.ArtistID, command.ContributorMemberIDs),
		Change: func(ctx context.Context, tx *gorm.DB) (TitleUpdate, error) {
			if title.target {
				return s.saveArtistTargetTitle(ctx, tx, title)
			}
			return s.saveArtistSourceTitle(ctx, tx, title)
		},
		Audit: func(TitleUpdate) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, tx *gorm.DB, update TitleUpdate) error {
			if !update.Result.Changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             command.ArtistID,
				Fields:               []string{"title"},
				DocumentRevision:     update.Result.DocumentRevision.String(),
				Contributors:         command.ContributorMemberIDs,
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               locale,
				TargetRevision:       update.TargetRevision,
				DocumentStateChanged: !title.target,
			}))
		},
	})
}

// artistRoomTitle is a validated locale title save.
type artistRoomTitle struct {
	artistID               string
	locale                 string
	target                 bool
	title                  *string
	expectedRevision       uuid.UUID
	expectedTargetRevision *string
	documentID             uuid.UUID
}

// prepareArtistTitle validates the save before its transaction, in the
// order each room kind always checked it.
func (s *Service) prepareArtistTitle(ctx context.Context, command UpdateArtistLocaleMetadataCommand, locale string, target bool) (artistRoomTitle, error) {
	title := artistRoomTitle{
		artistID: command.ArtistID, locale: locale, target: target, title: command.Title,
		expectedTargetRevision: command.ExpectedTargetRevision,
	}
	if !target {
		if command.ExpectedTargetRevision != nil {
			return artistRoomTitle{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
		}
		if command.Title == nil {
			return artistRoomTitle{}, domainerr.InvalidField("title", "is required")
		}
		normalized := strings.TrimSpace(*command.Title)
		if normalized == "" {
			return artistRoomTitle{}, domainerr.InvalidField("title", "must not be empty")
		}
		title.title = &normalized
	}
	var err error
	if title.expectedRevision, err = uuid.Parse(command.ExpectedRevision); err != nil {
		return artistRoomTitle{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	if title.documentID, err = loadCreativeContentDocumentID(ctx, s.db, command.ArtistID); err != nil {
		return artistRoomTitle{}, err
	}
	return title, nil
}

func (s *Service) saveArtistTargetTitle(ctx context.Context, tx *gorm.DB, title artistRoomTitle) (TitleUpdate, error) {
	result, targetRevision, err := updateArtistTargetLocaleTitle(
		ctx, tx, s.contentBlocks, title.artistID, title.documentID, title.locale,
		title.expectedRevision, title.expectedTargetRevision, title.title, time.Now().UTC(),
		internalCreativeContentFence(title.artistID),
	)
	if err != nil {
		return TitleUpdate{}, err
	}
	return TitleUpdate{Locale: title.locale, Result: result, TargetRevision: &targetRevision}, nil
}

// saveArtistSourceTitle advances the document revision with the new source
// title and refreshes the OG image when the translation source changed.
func (s *Service) saveArtistSourceTitle(ctx context.Context, tx *gorm.DB, title artistRoomTitle) (TitleUpdate, error) {
	now := time.Now().UTC()
	advanced, err := s.contentBlocks.AdvanceRevision(
		ctx, tx, contentblock.AdvanceInput{DocumentID: title.documentID, ExpectedRevision: title.expectedRevision},
		artistSourceRoomFence(title.artistID, title.locale),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return saveArtistSourceTitleRow(ctx, tx, title, now)
		},
	)
	if err != nil {
		return TitleUpdate{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	update := TitleUpdate{
		Locale: title.locale, SourceChanged: advanced.TranslationSourceChanged,
		Result: contentblock.Result{DocumentRevision: advanced.DocumentRevision, Changed: advanced.Changed, TranslationSourceChanged: advanced.TranslationSourceChanged},
	}
	if advanced.Changed {
		update.Result.ChangedLocales = []string{title.locale}
	}
	if advanced.TranslationSourceChanged {
		if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, title.artistID, "", false, artistContentEntity+"_source_title_saved"); err != nil {
			return TitleUpdate{}, err
		}
	}
	return update, nil
}

func saveArtistSourceTitleRow(ctx context.Context, tx *gorm.DB, title artistRoomTitle, now time.Time) (contentblock.MetadataEffect, error) {
	sourceAuthority, err := lockCreativeTranslationSourceContext(ctx, tx, artistContentEntity, title.artistID)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if sourceAuthority.SourceLocale != title.locale {
		return contentblock.MetadataEffect{}, domainerr.Precondition("only source locale metadata is editable")
	}
	current, err := loadCreativeLocaleMetadataState(ctx, tx, title.artistID, title.locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if current != nil && current.Title != nil && *current.Title == *title.title {
		return contentblock.MetadataEffect{}, nil
	}
	if err := saveArtistSourceLocaleDocumentState(ctx, tx, title.artistID, title.locale, translationLocaleDocumentSaveInput{
		SourceLocale: sourceAuthority.SourceLocale, Title: title.title, Now: now,
	}); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if err := touchArtistRootUpdatedAt(ctx, tx, title.artistID, now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func loadArtistDocumentMetadata(
	ctx context.Context,
	db *gorm.DB,
	artistID string,
) (*intrav1.ArtistDocumentMetadata, error) {
	var artist model.Artist
	if err := db.WithContext(ctx).
		Select("id", "real_name", "country_code", "website", "social_links", "slug", "parent_artist_id").
		Where("id = ?", artistID).
		Take(&artist).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("artist", artistID)
		}
		return nil, err
	}
	var labelIDs []string
	if err := db.WithContext(ctx).
		Table("artist_label").
		Select("label_id::text").
		Where("artist_id = ?", artistID).
		Order("label_id ASC").
		Scan(&labelIDs).Error; err != nil {
		return nil, err
	}
	return &intrav1.ArtistDocumentMetadata{
		RealName:       artist.RealName,
		CountryCode:    artist.CountryCode,
		Website:        artist.Website,
		SocialLinks:    artist.SocialLinks,
		Slug:           artist.Slug,
		LabelIds:       labelIDs,
		ParentArtistId: artist.ParentArtistID,
	}, nil
}

package artist

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// LoadArtistBlockDocumentQuery opens the Artist document of a collaboration
// room in Locale for the session SessionID.
type LoadArtistBlockDocumentQuery struct {
	ArtistID  string
	Locale    string
	SessionID string
}

// LocaleMetadata is the title of one Artist locale a room shows.
type LocaleMetadata struct {
	Locale string
	Title  *string
}

// RoomDocument is a collaboration room's Artist document with its locale
// and Artist metadata.
type RoomDocument struct {
	Document            *contentv1.LocalizedRichTextDocument
	DocumentRevision    string
	SourceMetadata      LocaleMetadata
	Metadata            DocumentMetadata
	Locale              string
	LocaleExists        bool
	LocaleMetadata      *LocaleMetadata
	TargetRevision      *string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
}

// LoadArtistBlockDocument returns the room's document with its locale and
// Artist metadata.
func (s *Service) LoadArtistBlockDocument(ctx context.Context, query LoadArtistBlockDocumentQuery) (RoomDocument, error) {
	locale, err := normalizeArtistDocumentLocale(query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	var localeState artistTargetLocaleState
	var metadata DocumentMetadata
	bootstrap, err := s.loadCreativeContentBlockBootstrap(ctx, query.ArtistID, query.SessionID, func(ctx context.Context, store Store) error {
		documentID, err := loadCreativeContentDocumentID(ctx, store, query.ArtistID)
		if err != nil {
			return err
		}
		if localeState, err = loadArtistTargetLocaleState(ctx, store, query.ArtistID, documentID, locale, false); err != nil {
			return err
		}
		metadata, err = loadArtistDocumentMetadata(ctx, store, query.ArtistID)
		return err
	})
	if err != nil {
		return RoomDocument{}, err
	}
	document, err := artistLocalizedDocument(localeState, locale)
	if err != nil {
		return RoomDocument{}, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, locale)
	if err != nil {
		return RoomDocument{}, err
	}
	room := RoomDocument{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: LocaleMetadata{Locale: localeState.SourceLocale, Title: localeState.SourceMetadata.Title},
		Metadata:       metadata, Locale: locale,
		LocaleExists: localeState.TargetMetadata != nil, PresentLocaleValues: presentLocaleValues,
	}
	if localeState.TargetMetadata != nil {
		room.LocaleMetadata = &LocaleMetadata{Locale: locale, Title: localeState.TargetMetadata.Title}
		if locale != localeState.SourceLocale {
			room.TargetRevision = &localeState.TargetRevision
		}
	}
	return room, nil
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
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[BlockBatchResult, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			batch.domain, err = internalCreativeContentFence(command.ArtistID)(ctx, store, batch.documentID)
			return err
		},
		Access: contributorAccess(command.ArtistID, contributors),
		Change: func(ctx context.Context, store Store) (BlockBatchResult, error) {
			batch.contributor = contributors[0]
			if batch.locale != batch.domain.SourceLocale {
				return applyArtistTargetRoomBatch(ctx, store, batch)
			}
			return applyArtistSourceRoomBatch(ctx, store, batch)
		},
		Audit: func(result BlockBatchResult) mutation.Audit {
			if !result.Content.Changed {
				return mutation.Unchanged()
			}
			return artistMemberLocaleAudit(batch.contributor, command.ArtistID, result.Locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, store Store, result BlockBatchResult) error {
			if !result.Content.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{ArtistID: command.ArtistID, Kind: LocaleUpdate, Locale: LocaleChange{
				Fields:               []string{"content"},
				DocumentRevision:     result.Content.DocumentRevision.String(),
				Contributors:         contributors,
				Source:               SourceCollaboration,
				Locale:               batch.locale,
				TargetRevision:       result.TargetRevision,
				DocumentStateChanged: batch.locale == result.SourceLocale,
			}})
		},
	})
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(err)
	}
	return result, nil
}

// prepareArtistRoomBatch validates the batch before its transaction.
func (s *Service) prepareArtistRoomBatch(ctx context.Context, command ApplyArtistBlockBatchCommand) (artistRoomBatch, error) {
	locale, err := normalizeArtistDocumentLocale(command.Locale)
	if err != nil {
		return artistRoomBatch{}, err
	}
	documentID, err := s.readCreativeContentDocumentID(ctx, command.ArtistID)
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
func contributorAccess(artistID string, contributors []string) mutation.Access {
	return mutation.Checked(func(ctx context.Context, store Store) error {
		if err := store.RequireDocumentContributors(ctx, contributors); err != nil {
			return err
		}
		return store.RequireCurrentContributors(ctx, artistID, contributors)
	})
}

func applyArtistTargetRoomBatch(ctx context.Context, store Store, b artistRoomBatch) (BlockBatchResult, error) {
	batch, err := contentblock.BatchFromRichTextStorage(b.documentID, b.input.Profile, b.storage)
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(err)
	}
	result, targetRevision, err := applyArtistTargetLocaleMutation(ctx, store, artistTargetLocaleMutationInput{
		ArtistID: b.artistID, DocumentID: b.documentID, Locale: b.locale, Batch: batch,
		ExpectedTargetRevision: b.expectedTargetRevision, Now: time.Now().UTC(),
		Fence: internalCreativeContentFence(b.artistID),
	})
	if err != nil {
		return BlockBatchResult{}, err
	}
	return BlockBatchResult{Locale: b.locale, SourceLocale: b.domain.SourceLocale, Content: result, TargetRevision: &targetRevision}, nil
}

func applyArtistSourceRoomBatch(ctx context.Context, store Store, b artistRoomBatch) (BlockBatchResult, error) {
	if b.expectedTargetRevision != nil {
		return BlockBatchResult{}, domainerr.InvalidField("expected_target_revision", "source Artist room cannot carry a target revision")
	}
	if err := validateArtistSourceStorage(b.storage, b.domain.SourceLocale); err != nil {
		return BlockBatchResult{}, err
	}
	batch, err := contentblock.BatchFromRichTextStorage(b.documentID, b.input.Profile, b.storage)
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(err)
	}
	result, err := store.ApplyContentBatch(ctx, batch, artistSourceRoomFence(b.artistID, b.domain.SourceLocale))
	if err != nil {
		return BlockBatchResult{}, normalizeCreativeContentBlockError(err)
	}
	if result.Changed {
		if err := store.StampArtist(ctx, b.artistID, time.Now().UTC()); err != nil {
			return BlockBatchResult{}, err
		}
	}
	if result.TranslationSourceChanged {
		if _, err := store.RequestOG(ctx, b.artistID, "", false, "artist_source_document_saved"); err != nil {
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
	sourceLocale, err := s.readCreativeDocumentAuthority(ctx, command.ArtistID)
	if err != nil {
		return TitleUpdate{}, err
	}
	title, err := s.prepareArtistTitle(ctx, command, locale, locale != sourceLocale)
	if err != nil {
		return TitleUpdate{}, err
	}
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[TitleUpdate, Store]{
		Access: contributorAccess(command.ArtistID, command.ContributorMemberIDs),
		Change: func(ctx context.Context, store Store) (TitleUpdate, error) {
			if title.target {
				return saveArtistTargetTitle(ctx, store, title)
			}
			return saveArtistSourceTitle(ctx, store, title)
		},
		Audit: func(TitleUpdate) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, store Store, update TitleUpdate) error {
			if !update.Result.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{ArtistID: command.ArtistID, Kind: LocaleUpdate, Locale: LocaleChange{
				Fields:               []string{"title"},
				DocumentRevision:     update.Result.DocumentRevision.String(),
				Contributors:         command.ContributorMemberIDs,
				Source:               SourceCollaboration,
				Locale:               locale,
				TargetRevision:       update.TargetRevision,
				DocumentStateChanged: !title.target,
			}})
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
	if title.documentID, err = s.readCreativeContentDocumentID(ctx, command.ArtistID); err != nil {
		return artistRoomTitle{}, err
	}
	return title, nil
}

func saveArtistTargetTitle(ctx context.Context, store Store, title artistRoomTitle) (TitleUpdate, error) {
	result, targetRevision, err := updateArtistTargetLocaleTitle(
		ctx, store, title.artistID, title.documentID, title.locale,
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
func saveArtistSourceTitle(ctx context.Context, store Store, title artistRoomTitle) (TitleUpdate, error) {
	now := time.Now().UTC()
	advanced, err := store.AdvanceContentRevision(
		ctx, contentblock.AdvanceInput{DocumentID: title.documentID, ExpectedRevision: title.expectedRevision},
		artistSourceRoomFence(title.artistID, title.locale),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			return saveArtistSourceTitleRow(ctx, store, title, now)
		},
	)
	if err != nil {
		return TitleUpdate{}, normalizeCreativeContentBlockError(err)
	}
	update := TitleUpdate{
		Locale: title.locale, SourceChanged: advanced.TranslationSourceChanged,
		Result: contentblock.Result{DocumentRevision: advanced.DocumentRevision, Changed: advanced.Changed, TranslationSourceChanged: advanced.TranslationSourceChanged},
	}
	if advanced.Changed {
		update.Result.ChangedLocales = []string{title.locale}
	}
	if advanced.TranslationSourceChanged {
		if _, err := store.RequestOG(ctx, title.artistID, "", false, artistContentEntity+"_source_title_saved"); err != nil {
			return TitleUpdate{}, err
		}
	}
	return update, nil
}

func saveArtistSourceTitleRow(ctx context.Context, store Store, title artistRoomTitle, now time.Time) (contentblock.MetadataEffect, error) {
	sourceAuthority, err := lockCreativeTranslationSourceContext(ctx, store, title.artistID)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if sourceAuthority.SourceLocale != title.locale {
		return contentblock.MetadataEffect{}, domainerr.Precondition("only source locale metadata is editable")
	}
	current, exists, err := store.LocaleTitleValue(ctx, title.artistID, title.locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists && current != nil && *current == *title.title {
		return contentblock.MetadataEffect{}, nil
	}
	if err := saveCreativeSourceLocaleMetadata(ctx, store, title.artistID, title.locale, translationLocaleDocumentSaveInput{
		SourceLocale: sourceAuthority.SourceLocale, Title: title.title, Now: now,
	}); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if err := touchArtist(ctx, store, title.artistID, now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func loadArtistDocumentMetadata(ctx context.Context, store Store, artistID string) (DocumentMetadata, error) {
	metadata, err := store.DocumentMetadata(ctx, artistID)
	if errors.Is(err, ErrArtistNotFound) {
		return DocumentMetadata{}, domainerr.ResourceNotFound("artist", artistID)
	}
	return metadata, err
}

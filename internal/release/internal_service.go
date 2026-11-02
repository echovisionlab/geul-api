package release

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// CollaborationPrincipal is the session a collaboration room was opened
// with.
type CollaborationPrincipal struct {
	SessionID string
}

// LoadReleaseBlockDocumentQuery names the Release locale a collaboration
// room opens and the room's principal.
type LoadReleaseBlockDocumentQuery struct {
	ReleaseID string
	Locale    string
	Principal *CollaborationPrincipal
}

// ApplyReleaseBlockBatchCommand stores one collaboration Block batch of a
// Release locale.
type ApplyReleaseBlockBatchCommand struct {
	ReleaseID string
	Locale    string
	// Batch carries the expected revision and the contributors it is
	// attributed to.
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
	ExpectedTargetRevision *string
}

// CreditNote is the note of one Release credit in a locale.
type CreditNote struct {
	CreditID string
	Note     string
}

// CreditNotes replaces every credit note of a locale.
type CreditNotes struct {
	Values []*CreditNote
}

// UpdateReleaseLocaleMetadataCommand stores the title or credit notes of a
// Release locale from a collaboration room.
type UpdateReleaseLocaleMetadataCommand struct {
	ReleaseID              string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	Title                  *string
	CreditNotes            *CreditNotes
	ContributorMemberIDs   []string
}

// DocumentSave is the document state after a collaboration save.
// TargetRevision is set for a target locale only.
type DocumentSave struct {
	Locale           string
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	TargetRevision   *string
}

// LocaleNotes is a stored Release locale with its credit notes.
type LocaleNotes struct {
	Metadata LocaleMetadata
	Notes    map[string]string
}

// RoomArtist is one Artist of a Release a collaboration room shows.
type RoomArtist struct {
	ArtistID  string
	Name      string
	Slug      *string
	SortOrder int32
}

// BlockDocument is the effective locale view a collaboration room edits.
// LocaleMetadata is nil for a locale without metadata, and TargetRevision
// is set for an existing target locale only.
type BlockDocument struct {
	Document            *contentv1.LocalizedRichTextDocument
	DocumentRevision    string
	SourceMetadata      LocaleNotes
	Artists             []RoomArtist
	Locale              string
	LocaleExists        bool
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
	LocaleMetadata      *LocaleNotes
	TargetRevision      *string
}

// LoadReleaseBlockDocument reads the effective locale view a collaboration
// room edits.
func (s *Service) LoadReleaseBlockDocument(ctx context.Context, query LoadReleaseBlockDocumentQuery) (BlockDocument, error) {
	locale, err := normalizeReleaseDocumentLocale(query.Locale)
	if err != nil {
		return BlockDocument{}, err
	}
	var metadata releaseRoomMetadata
	bootstrap, err := s.loadReleaseContentBlockBootstrap(ctx, query.ReleaseID, query.Principal,
		func(ctx context.Context, store Store) error {
			var err error
			metadata, err = loadReleaseRoomMetadata(ctx, store, query.ReleaseID, locale)
			return err
		},
	)
	if err != nil {
		return BlockDocument{}, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, locale)
	if err != nil {
		return BlockDocument{}, err
	}
	if metadata.target == nil && targetlocale.SnapshotHasLocale(bootstrap.Snapshot, locale) {
		return BlockDocument{}, domainerr.Precondition("Release target locale Blocks exist without owning metadata")
	}
	// Collaboration renders the effective locale view: target values override
	// source values and an absent target value falls back to the source value.
	// Presence remains the sparse persisted target projection above, so the
	// client can still distinguish fallback from an explicit empty value.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(bootstrap.Snapshot, locale)
	if err != nil {
		return BlockDocument{}, normalizeReleaseContentBlockError(err)
	}
	response := BlockDocument{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: LocaleNotes{Metadata: metadata.source, Notes: metadata.sourceNotes}, Artists: metadata.artists,
		Locale: locale, LocaleExists: metadata.target != nil,
		PresentLocaleValues: presentLocaleValues,
	}
	if metadata.target == nil {
		return response, nil
	}
	response.LocaleMetadata = &LocaleNotes{Metadata: *metadata.target, Notes: metadata.targetNotes}
	if locale != bootstrap.SourceLocale {
		targetRevision, err := deriveReleaseTargetRevision(response.DocumentRevision, *metadata.target)
		if err != nil {
			return BlockDocument{}, err
		}
		response.TargetRevision = &targetRevision
	}
	return response, nil
}

// releaseRoomMetadata is the locale metadata and Artists a collaboration room
// shows next to the document. target is nil for a locale without metadata.
type releaseRoomMetadata struct {
	source      LocaleMetadata
	sourceNotes map[string]string
	target      *LocaleMetadata
	targetNotes map[string]string
	artists     []RoomArtist
}

func loadReleaseRoomMetadata(ctx context.Context, store Store, releaseID, locale string) (releaseRoomMetadata, error) {
	sourceLocale, err := loadReleaseSourceLocale(ctx, store, releaseID, Unlocked)
	if err != nil {
		return releaseRoomMetadata{}, err
	}
	var metadata releaseRoomMetadata
	var sourceExists bool
	metadata.source, sourceExists, err = store.LocaleMetadata(ctx, releaseID, sourceLocale, false)
	if err != nil {
		return releaseRoomMetadata{}, err
	}
	if !sourceExists {
		return releaseRoomMetadata{}, domainerr.Precondition("Release source locale metadata is missing")
	}
	metadata.sourceNotes, err = store.CreditNotes(ctx, releaseID, sourceLocale)
	if err != nil {
		return releaseRoomMetadata{}, err
	}
	if locale == sourceLocale {
		target := metadata.source
		metadata.target, metadata.targetNotes = &target, maps.Clone(metadata.sourceNotes)
	} else {
		row, exists, err := store.LocaleMetadata(ctx, releaseID, locale, false)
		if err != nil {
			return releaseRoomMetadata{}, err
		}
		if exists {
			metadata.target = &row
			if metadata.targetNotes, err = store.CreditNotes(ctx, releaseID, locale); err != nil {
				return releaseRoomMetadata{}, err
			}
		}
	}
	metadata.artists, err = loadReleaseArtistStates(ctx, store, releaseID)
	return metadata, err
}

// collaborationSave is one collaboration save of a Release locale: what to
// answer, which locale's audit record to append when it changed, and why OG
// work must be requested, if it must.
type collaborationSave struct {
	save        DocumentSave
	auditLocale string
	ogReason    string
}

// saveCollaborationDocument runs one collaboration save behind the Release
// root lock. The save's authority is its contributors.
func (s *Service) saveCollaborationDocument(
	ctx context.Context,
	releaseID string,
	contributors []string,
	save func(context.Context, Store, string) (collaborationSave, error),
) (DocumentSave, error) {
	var sourceLocale string
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[collaborationSave, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			sourceLocale, err = loadReleaseSourceLocale(ctx, store, releaseID, UpdateLock)
			return err
		},
		Access: s.collaborationContributorsAccess(releaseID, contributors),
		Change: func(ctx context.Context, store Store) (collaborationSave, error) {
			return save(ctx, store, sourceLocale)
		},
		Audit: func(result collaborationSave) mutation.Audit {
			if !result.save.Changed {
				return mutation.Unchanged()
			}
			return releaseRequestLocaleAudit(releaseID, result.auditLocale)
		},
		Outbox: func(ctx context.Context, store Store, result collaborationSave) error {
			if result.ogReason == "" {
				return nil
			}
			return store.RequestOG(ctx, releaseID, result.ogReason)
		},
	})
	if err != nil {
		return DocumentSave{}, normalizeReleaseContentBlockError(err)
	}
	return result.save, nil
}

// ApplyReleaseBlockBatch stores one collaboration Block batch. A target
// locale may only change its own locale values; the source locale may
// change the shared Block graph.
func (s *Service) ApplyReleaseBlockBatch(ctx context.Context, command ApplyReleaseBlockBatchCommand) (DocumentSave, error) {
	if command.Batch == nil {
		return DocumentSave{}, domainerr.Required("batch")
	}
	locale, err := normalizeReleaseDocumentLocale(command.Locale)
	if err != nil {
		return DocumentSave{}, err
	}
	documentID, err := s.readReleaseContentDocumentID(ctx, command.ReleaseID)
	if err != nil {
		return DocumentSave{}, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(command.Batch, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
	if err != nil {
		return DocumentSave{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return DocumentSave{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	expected, err := uuid.Parse(command.Batch.ExpectedRevision)
	if err != nil || expected == uuid.Nil {
		return DocumentSave{}, domainerr.InvalidField("batch.expected_revision", "must be a UUID")
	}
	batch := releaseBlockBatch{command: command, locale: locale, documentID: documentID, expected: expected, storage: storage}
	return s.saveCollaborationDocument(ctx, command.ReleaseID, command.Batch.ContributorMemberIds,
		func(ctx context.Context, store Store, sourceLocale string) (collaborationSave, error) {
			if locale != sourceLocale {
				return applyTargetBlockBatch(ctx, store, batch)
			}
			return applySourceBlockBatch(ctx, store, batch, sourceLocale)
		})
}

// releaseBlockBatch is a validated collaboration Block batch.
type releaseBlockBatch struct {
	command    ApplyReleaseBlockBatchCommand
	locale     string
	documentID uuid.UUID
	expected   uuid.UUID
	storage    contentv1.ContentStorageMutationBatch
}

func applyTargetBlockBatch(ctx context.Context, store Store, input releaseBlockBatch) (collaborationSave, error) {
	if len(input.storage.BaseUpserts) != 0 || len(input.storage.Deletes) != 0 || len(input.storage.Moves) != 0 {
		return collaborationSave{}, domainerr.CollaborationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN, "Release target locale cannot mutate the shared Block graph")
	}
	for _, group := range input.storage.LocaleGroups {
		if group.Locale != input.locale {
			return collaborationSave{}, domainerr.CollaborationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH, "Release target locale mutation must match the authenticated room locale")
		}
	}
	batch, err := contentblock.BatchFromRichTextStorage(input.documentID, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, input.storage)
	if err != nil {
		return collaborationSave{}, err
	}
	target, err := applyReleaseTargetLocaleMutation(ctx, store, releaseTargetLocaleMutationInput{
		ReleaseID: input.command.ReleaseID, DocumentID: input.documentID, Locale: input.locale,
		ExpectedDocumentRevision: input.expected, ExpectedTargetRevision: input.command.ExpectedTargetRevision,
		Batch: batch, Now: time.Now().UTC(), Fence: internalReleaseContentFence(input.command.ReleaseID),
	})
	if err != nil {
		return collaborationSave{}, err
	}
	return collaborationSave{save: targetDocumentSave(input.locale, target), auditLocale: input.locale}, nil
}

func applySourceBlockBatch(ctx context.Context, store Store, input releaseBlockBatch, sourceLocale string) (collaborationSave, error) {
	if input.command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source Release room cannot carry a target revision")
	}
	for _, group := range input.storage.LocaleGroups {
		if group.Locale != sourceLocale {
			return collaborationSave{}, domainerr.CollaborationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH, "Release source mutation must match the current source locale")
		}
	}
	batch, err := contentblock.BatchFromRichTextProto(input.documentID, input.command.Batch)
	if err != nil {
		return collaborationSave{}, err
	}
	result, err := store.ApplyContentBatch(ctx, batch, internalReleaseContentFence(input.command.ReleaseID))
	if err != nil {
		return collaborationSave{}, err
	}
	saved := collaborationSave{
		save: DocumentSave{
			Locale: input.locale, DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales,
		},
		auditLocale: sourceLocale,
	}
	if result.Changed && result.TranslationSourceChanged {
		saved.ogReason = "release_source_document_saved"
	}
	return saved, nil
}

func targetDocumentSave(locale string, target releaseTargetLocaleMutationResult) DocumentSave {
	revision := target.TargetRevision
	return DocumentSave{
		Locale: locale, DocumentRevision: target.Content.DocumentRevision.String(), Changed: target.Content.Changed,
		ChangedLocales: target.Content.ChangedLocales, TargetRevision: &revision,
	}
}

// UpdateReleaseLocaleMetadata stores a collaboration room's title or credit
// notes of one locale.
func (s *Service) UpdateReleaseLocaleMetadata(ctx context.Context, command UpdateReleaseLocaleMetadataCommand) (DocumentSave, error) {
	if command.Title == nil && command.CreditNotes == nil {
		return DocumentSave{}, domainerr.InvalidField("update", "at least one metadata value is required")
	}
	locale, err := normalizeReleaseDocumentLocale(command.Locale)
	if err != nil {
		return DocumentSave{}, err
	}
	expectedRevision, err := uuid.Parse(command.ExpectedRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return DocumentSave{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	documentID, err := s.readReleaseContentDocumentID(ctx, command.ReleaseID)
	if err != nil {
		return DocumentSave{}, err
	}
	update := releaseMetadataUpdate{
		command: command, locale: locale, documentID: documentID, expected: expectedRevision, now: time.Now().UTC(),
	}
	return s.saveCollaborationDocument(ctx, command.ReleaseID, command.ContributorMemberIDs,
		func(ctx context.Context, store Store, sourceLocale string) (collaborationSave, error) {
			currentCreditIDs, err := store.CreditIDs(ctx, command.ReleaseID)
			if err != nil {
				return collaborationSave{}, err
			}
			if command.CreditNotes != nil {
				values, err := validateReleaseCreditNotes(command.CreditNotes.Values, currentCreditIDs, locale != sourceLocale)
				if err != nil {
					return collaborationSave{}, err
				}
				update.notes = &values
			}
			if locale != sourceLocale {
				return updateTargetLocaleMetadata(ctx, store, update)
			}
			return updateSourceLocaleMetadata(ctx, store, update, sourceLocale)
		})
}

// releaseMetadataUpdate is a validated collaboration metadata update; notes
// replace the locale's credit notes when set.
type releaseMetadataUpdate struct {
	command    UpdateReleaseLocaleMetadataCommand
	locale     string
	documentID uuid.UUID
	expected   uuid.UUID
	now        time.Time
	notes      *map[string]string
}

func updateTargetLocaleMetadata(ctx context.Context, store Store, update releaseMetadataUpdate) (collaborationSave, error) {
	command := update.command
	target, err := applyReleaseTargetLocaleMutation(ctx, store, releaseTargetLocaleMutationInput{
		ReleaseID: command.ReleaseID, DocumentID: update.documentID, Locale: update.locale,
		ExpectedDocumentRevision: update.expected, ExpectedTargetRevision: command.ExpectedTargetRevision,
		Batch: contentblock.Batch{
			DocumentID: update.documentID, ExpectedRevision: update.expected,
			ContributorMemberIDs: releaseContributorUUIDs(command.ContributorMemberIDs),
		},
		SetTitle: command.Title != nil, Title: command.Title, ReplaceCreditNotes: update.notes,
		Now: update.now, Fence: internalReleaseContentFence(command.ReleaseID),
	})
	if err != nil {
		return collaborationSave{}, err
	}
	return collaborationSave{save: targetDocumentSave(update.locale, target), auditLocale: update.locale}, nil
}

func updateSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	update releaseMetadataUpdate,
	sourceLocale string,
) (collaborationSave, error) {
	command := update.command
	if command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source Release room cannot carry a target revision")
	}
	write := sourceMetadataWrite{update: update, sourceLocale: sourceLocale}
	result, err := store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: update.documentID, ExpectedRevision: update.expected},
		internalReleaseContentFence(command.ReleaseID),
		write.apply,
	)
	if err != nil {
		return collaborationSave{}, normalizeReleaseContentBlockError(err)
	}
	saved := collaborationSave{
		save: DocumentSave{
			Locale: update.locale, DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: result.TranslationSourceChanged, ChangedLocales: []string{sourceLocale},
		},
		auditLocale: sourceLocale,
	}
	if result.Changed && result.TranslationSourceChanged && write.titleChanged {
		saved.ogReason = "release_source_title_saved"
	}
	return saved, nil
}

// sourceMetadataWrite stores a source locale title and credit notes once the
// document revision advanced, and keeps whether the title changed.
type sourceMetadataWrite struct {
	update       releaseMetadataUpdate
	sourceLocale string
	titleChanged bool
}

func (w *sourceMetadataWrite) apply(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
	releaseID := w.update.command.ReleaseID
	current, exists, err := store.LocaleMetadata(ctx, releaseID, w.sourceLocale, false)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	currentNotes, err := store.CreditNotes(ctx, releaseID, w.sourceLocale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	var currentTitle *string
	if exists {
		currentTitle = current.Title
	}
	nextTitle := currentTitle
	if title := w.update.command.Title; title != nil {
		normalizedTitle := strings.TrimSpace(*title)
		if normalizedTitle == "" {
			return contentblock.MetadataEffect{}, domainerr.InvalidField("title", "must not be empty")
		}
		nextTitle = &normalizedTitle
	}
	resolvedNotes := currentNotes
	if w.update.notes != nil {
		resolvedNotes = maps.Clone(*w.update.notes)
	}
	titleChanged := !releaseOptionalStringsEqual(currentTitle, nextTitle)
	notesChanged := !maps.Equal(currentNotes, resolvedNotes)
	if !titleChanged && !notesChanged {
		return contentblock.MetadataEffect{}, nil
	}
	if err := saveReleaseSourceLocaleMetadata(ctx, store, releaseID, w.sourceLocale, translationLocaleDocumentSaveInput{
		Title: nextTitle, Now: w.update.now,
	}); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	w.titleChanged = titleChanged
	if notesChanged {
		if err := store.ReplaceCreditNotes(ctx, releaseID, w.sourceLocale, resolvedNotes, w.update.now); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	}
	if err := touchReleaseSourceLocaleState(ctx, store, releaseID, w.update.now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func releaseContributorUUIDs(values []string) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if parsed, err := uuid.Parse(value); err == nil && parsed != uuid.Nil {
			result = append(result, parsed)
		}
	}
	return result
}

func validateReleaseCreditNotes(
	values []*CreditNote,
	currentCreditIDs []string,
	allowEmpty bool,
) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(currentCreditIDs))
	for _, creditID := range currentCreditIDs {
		allowed[creditID] = struct{}{}
	}
	notes := make(map[string]string, len(values))
	for _, value := range values {
		if value == nil {
			return nil, domainerr.InvalidField("credit_notes", "must not contain null entries")
		}
		creditID := strings.TrimSpace(value.CreditID)
		note := strings.TrimSpace(value.Note)
		if creditID == "" {
			return nil, domainerr.InvalidField("credit_notes", "credit_id is required")
		}
		if note == "" && !allowEmpty {
			return nil, domainerr.InvalidField("credit_notes", "note must not be empty")
		}
		if _, ok := allowed[creditID]; !ok {
			return nil, domainerr.InvalidField("credit_notes", "credit_id does not belong to the Release")
		}
		if _, duplicate := notes[creditID]; duplicate {
			return nil, domainerr.InvalidField("credit_notes", "credit_id must be unique")
		}
		notes[creditID] = note
	}
	return notes, nil
}

func releaseOptionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func loadReleaseArtistStates(ctx context.Context, store Store, releaseID string) ([]RoomArtist, error) {
	rows, err := store.ReleaseArtistsByCreation(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArtistID)
	}
	summaries, err := store.ArtistSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	artists := make([]RoomArtist, 0, len(rows))
	for _, row := range rows {
		summary := summaries[row.ArtistID]
		var slug *string
		if summary.Slug != "" {
			slug = &summary.Slug
		}
		artists = append(artists, RoomArtist{
			ArtistID: row.ArtistID, Name: summary.Title, Slug: slug, SortOrder: int32(row.SortOrder),
		})
	}
	return artists, nil
}

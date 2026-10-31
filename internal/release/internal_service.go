package release

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/collaborationconflict"
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

// LoadReleaseBlockDocument reads the effective locale view a collaboration
// room edits.
func (s *Service) LoadReleaseBlockDocument(ctx context.Context, query LoadReleaseBlockDocumentQuery) (*intrav1.LoadReleaseBlockDocumentResponse, error) {
	locale, err := normalizeReleaseDocumentLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	var metadata releaseRoomMetadata
	bootstrap, err := loadReleaseContentBlockBootstrap(
		ctx, s.db, s.contentBlocks, s.spiceDB, query.ReleaseID, query.Principal,
		func(ctx context.Context, tx *gorm.DB) error {
			var err error
			metadata, err = s.loadReleaseRoomMetadata(ctx, tx, query.ReleaseID, locale)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, locale)
	if err != nil {
		return nil, err
	}
	if metadata.target == nil && targetlocale.SnapshotHasLocale(bootstrap.Snapshot, locale) {
		return nil, domainerr.Precondition("Release target locale Blocks exist without owning metadata")
	}
	// Collaboration renders the effective locale view: target values override
	// source values and an absent target value falls back to the source value.
	// Presence remains the sparse persisted target projection above, so the
	// client can still distinguish fallback from an explicit empty value.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(bootstrap.Snapshot, locale)
	if err != nil {
		return nil, normalizeReleaseContentBlockError(err)
	}
	response := &intrav1.LoadReleaseBlockDocumentResponse{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: releaseLocaleMetadataMessage(metadata.source, metadata.sourceNotes), Artists: metadata.artists,
		Locale: locale, LocaleExists: metadata.target != nil,
		PresentLocaleValues: presentLocaleValues,
	}
	if metadata.target == nil {
		return response, nil
	}
	response.LocaleMetadata = releaseLocaleMetadataMessage(*metadata.target, metadata.targetNotes)
	if locale != bootstrap.Source.SourceLocale {
		targetRevision, err := deriveReleaseTargetRevision(response.DocumentRevision, *metadata.target)
		if err != nil {
			return nil, err
		}
		response.TargetRevision = &targetRevision
	}
	return response, nil
}

// releaseRoomMetadata is the locale metadata and Artists a collaboration room
// shows next to the document. target is nil for a locale without metadata.
type releaseRoomMetadata struct {
	source      releaseLocaleMetadataRow
	sourceNotes map[string]string
	target      *releaseLocaleMetadataRow
	targetNotes map[string]string
	artists     []*intrav1.ReleaseArtistState
}

func (s *Service) loadReleaseRoomMetadata(ctx context.Context, tx *gorm.DB, releaseID, locale string) (releaseRoomMetadata, error) {
	source, err := loadReleaseSourceLocale(ctx, tx, releaseID)
	if err != nil {
		return releaseRoomMetadata{}, err
	}
	var metadata releaseRoomMetadata
	var sourceExists bool
	metadata.source, sourceExists, err = loadOptionalReleaseLocaleMetadataRow(ctx, tx, releaseID, source.SourceLocale, false)
	if err != nil {
		return releaseRoomMetadata{}, err
	}
	if !sourceExists {
		return releaseRoomMetadata{}, domainerr.Precondition("Release source locale metadata is missing")
	}
	metadata.sourceNotes, err = loadReleaseCreditLocaleNotes(ctx, tx, releaseID, source.SourceLocale)
	if err != nil {
		return releaseRoomMetadata{}, err
	}
	if locale == source.SourceLocale {
		target := metadata.source
		metadata.target, metadata.targetNotes = &target, maps.Clone(metadata.sourceNotes)
	} else {
		row, exists, err := loadOptionalReleaseLocaleMetadataRow(ctx, tx, releaseID, locale, false)
		if err != nil {
			return releaseRoomMetadata{}, err
		}
		if exists {
			metadata.target = &row
			if metadata.targetNotes, err = loadReleaseCreditLocaleNotes(ctx, tx, releaseID, locale); err != nil {
				return releaseRoomMetadata{}, err
			}
		}
	}
	metadata.artists, err = s.loadReleaseArtistStates(ctx, tx, releaseID)
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
	save func(context.Context, *gorm.DB, releaseSourceLocale) (collaborationSave, error),
) (DocumentSave, error) {
	var source releaseSourceLocale
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[collaborationSave]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			source, err = loadReleaseSourceLocaleWithStrength(ctx, tx, releaseID, clause.LockingStrengthUpdate)
			return err
		},
		Access: s.collaborationContributorsAccess(releaseID, contributors),
		Change: func(ctx context.Context, tx *gorm.DB) (collaborationSave, error) {
			return save(ctx, tx, source)
		},
		Audit: func(result collaborationSave) mutation.Audit {
			if !result.save.Changed {
				return mutation.Unchanged()
			}
			return releaseRequestLocaleAudit(releaseID, result.auditLocale)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, result collaborationSave) error {
			if result.ogReason == "" {
				return nil
			}
			return s.contentOG.RequestCurrent(ctx, tx, releaseID, result.ogReason)
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
	documentID, err := loadReleaseContentDocumentID(ctx, s.db, command.ReleaseID)
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
		func(ctx context.Context, tx *gorm.DB, source releaseSourceLocale) (collaborationSave, error) {
			if locale != source.SourceLocale {
				return s.applyTargetBlockBatch(ctx, tx, batch)
			}
			return s.applySourceBlockBatch(ctx, tx, batch, source)
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

func (s *Service) applyTargetBlockBatch(ctx context.Context, tx *gorm.DB, input releaseBlockBatch) (collaborationSave, error) {
	if len(input.storage.BaseUpserts) != 0 || len(input.storage.Deletes) != 0 || len(input.storage.Moves) != 0 {
		return collaborationSave{}, collaborationconflict.MutationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN, "Release target locale cannot mutate the shared Block graph")
	}
	for _, group := range input.storage.LocaleGroups {
		if group.Locale != input.locale {
			return collaborationSave{}, collaborationconflict.MutationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH, "Release target locale mutation must match the authenticated room locale")
		}
	}
	batch, err := contentblock.BatchFromRichTextStorage(input.documentID, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, input.storage)
	if err != nil {
		return collaborationSave{}, err
	}
	target, err := applyReleaseTargetLocaleMutation(ctx, tx, s.contentBlocks, releaseTargetLocaleMutationInput{
		ReleaseID: input.command.ReleaseID, DocumentID: input.documentID, Locale: input.locale,
		ExpectedDocumentRevision: input.expected, ExpectedTargetRevision: input.command.ExpectedTargetRevision,
		Batch: batch, Now: time.Now().UTC(), Fence: internalReleaseContentFence(input.command.ReleaseID),
	})
	if err != nil {
		return collaborationSave{}, err
	}
	return collaborationSave{save: targetDocumentSave(input.locale, target), auditLocale: input.locale}, nil
}

func (s *Service) applySourceBlockBatch(ctx context.Context, tx *gorm.DB, input releaseBlockBatch, source releaseSourceLocale) (collaborationSave, error) {
	if input.command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source Release room cannot carry a target revision")
	}
	for _, group := range input.storage.LocaleGroups {
		if group.Locale != source.SourceLocale {
			return collaborationSave{}, collaborationconflict.MutationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH, "Release source mutation must match the current source locale")
		}
	}
	batch, err := contentblock.BatchFromRichTextProto(input.documentID, input.command.Batch)
	if err != nil {
		return collaborationSave{}, err
	}
	result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, internalReleaseContentFence(input.command.ReleaseID))
	if err != nil {
		return collaborationSave{}, err
	}
	saved := collaborationSave{
		save: DocumentSave{
			Locale: input.locale, DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales,
		},
		auditLocale: source.SourceLocale,
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
	documentID, err := loadReleaseContentDocumentID(ctx, s.db, command.ReleaseID)
	if err != nil {
		return DocumentSave{}, err
	}
	update := releaseMetadataUpdate{
		command: command, locale: locale, documentID: documentID, expected: expectedRevision, now: time.Now().UTC(),
	}
	return s.saveCollaborationDocument(ctx, command.ReleaseID, command.ContributorMemberIDs,
		func(ctx context.Context, tx *gorm.DB, source releaseSourceLocale) (collaborationSave, error) {
			currentCreditIDs, err := loadReleaseCreditIDs(ctx, tx, command.ReleaseID)
			if err != nil {
				return collaborationSave{}, err
			}
			if command.CreditNotes != nil {
				values, err := validateReleaseCreditNotes(command.CreditNotes.Values, currentCreditIDs, locale != source.SourceLocale)
				if err != nil {
					return collaborationSave{}, err
				}
				update.notes = &values
			}
			if locale != source.SourceLocale {
				return s.updateTargetLocaleMetadata(ctx, tx, update)
			}
			return s.updateSourceLocaleMetadata(ctx, tx, update, source)
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

func (s *Service) updateTargetLocaleMetadata(ctx context.Context, tx *gorm.DB, update releaseMetadataUpdate) (collaborationSave, error) {
	command := update.command
	target, err := applyReleaseTargetLocaleMutation(ctx, tx, s.contentBlocks, releaseTargetLocaleMutationInput{
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

func (s *Service) updateSourceLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	update releaseMetadataUpdate,
	source releaseSourceLocale,
) (collaborationSave, error) {
	command := update.command
	if command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source Release room cannot carry a target revision")
	}
	write := sourceMetadataWrite{update: update, sourceLocale: source.SourceLocale}
	result, err := s.contentBlocks.AdvanceRevision(
		ctx, tx,
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
			SourceChanged: result.TranslationSourceChanged, ChangedLocales: []string{source.SourceLocale},
		},
		auditLocale: source.SourceLocale,
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

func (w *sourceMetadataWrite) apply(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
	releaseID := w.update.command.ReleaseID
	current, err := loadReleaseLocaleMetadataState(ctx, tx, releaseID, w.sourceLocale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	currentNotes, err := loadReleaseCreditLocaleNotes(ctx, tx, releaseID, w.sourceLocale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	var currentTitle *string
	if current != nil {
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
	if err := saveReleaseSourceLocaleMetadata(ctx, tx, releaseID, w.sourceLocale, translationLocaleDocumentSaveInput{
		Title: nextTitle, Now: w.update.now,
	}); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	w.titleChanged = titleChanged
	if notesChanged {
		if err := replaceReleaseCreditLocaleNotes(ctx, tx, releaseID, w.sourceLocale, resolvedNotes, w.update.now); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	}
	if err := touchReleaseSourceLocaleState(ctx, tx, releaseID, w.update.now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func releaseLocaleMetadataMessage(row releaseLocaleMetadataRow, notes map[string]string) *intrav1.ReleaseLocaleMetadata {
	creditIDs := make([]string, 0, len(notes))
	for creditID := range notes {
		creditIDs = append(creditIDs, creditID)
	}
	sort.Strings(creditIDs)
	message := &intrav1.ReleaseLocaleMetadata{Locale: row.Locale, Title: row.Title}
	for _, creditID := range creditIDs {
		message.CreditNotes = append(message.CreditNotes, &intrav1.ReleaseCreditNote{CreditId: creditID, Note: notes[creditID]})
	}
	return message
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

func (s *Service) loadReleaseArtistStates(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
) ([]*intrav1.ReleaseArtistState, error) {
	var rows []struct {
		ArtistID  string `gorm:"column:artist_id"`
		SortOrder int32  `gorm:"column:sort_order"`
	}
	if err := db.WithContext(ctx).
		Table("release_artist").
		Select("release_artist.artist_id, release_artist.sort_order").
		Where("release_artist.release_id = ?", releaseID).
		Order("release_artist.sort_order ASC, release_artist.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArtistID)
	}
	summaries, err := s.artists.LoadArtistSummariesWithDB(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	artists := make([]*intrav1.ReleaseArtistState, 0, len(rows))
	for _, row := range rows {
		summary := summaries[row.ArtistID]
		var slug *string
		if summary.Slug != "" {
			slug = &summary.Slug
		}
		artists = append(artists, &intrav1.ReleaseArtistState{
			ArtistId: row.ArtistID, ArtistName: summary.Title,
			ArtistSlug: slug, SortOrder: row.SortOrder,
		})
	}
	return artists, nil
}

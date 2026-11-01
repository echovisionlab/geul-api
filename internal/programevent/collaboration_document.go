package programevent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// The collaboration service exchanges typed Blocks with these use cases. Yjs
// state stays resident in editor-collab and never crosses them. Their
// commands carry the generated Rich Text contract, which is the content
// model contentblock itself consumes.

// LoadBlockDocumentQuery opens one locale of an event's document for the
// collaboration session SessionID names; a nil SessionID has no principal.
type LoadBlockDocumentQuery struct {
	EventID   string
	Locale    string
	SessionID *string
}

// BlockLocaleMetadata is the title and summary of one locale a room shows.
type BlockLocaleMetadata struct {
	Locale  string
	Title   *string
	Summary *string
}

// RoomDocument is a collaboration room's document: the locale projection of
// the event's document, its source and room locale metadata, and the
// session's delivery of its Block media.
type RoomDocument struct {
	Document            *contentv1.LocalizedRichTextDocument
	DocumentRevision    string
	SourceMetadata      *BlockLocaleMetadata
	BlockMedia          []*contentv1.ContentBlockMediaItem
	Locale              string
	LocaleExists        bool
	LocaleMetadata      *BlockLocaleMetadata
	TargetRevision      *string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
}

// LoadBlockDocument authorizes the session's principal to view the event and
// returns the locale projection of its document.
func (s *Service) LoadBlockDocument(ctx context.Context, query LoadBlockDocumentQuery) (RoomDocument, error) {
	locale, err := normalizeProgramEventDocumentLocale(query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	var localeState programEventExactLocaleState
	var sourceMetadata *BlockLocaleMetadata
	var blockMedia []*contentv1.ContentBlockMediaItem
	if err := s.units.ReadCurrent(ctx, func(store Store) error {
		documentID, principal, err := requireProgramEventBlockBootstrap(ctx, store, s.spiceDB, query.EventID, query.SessionID)
		if err != nil {
			return err
		}
		sourceState, err := loadProgramEventSourceLocale(ctx, store, query.EventID, false)
		if err != nil {
			return err
		}
		localeState, err = loadProgramEventExactLocaleState(
			ctx, store, query.EventID, documentID, locale, false,
		)
		if err != nil {
			return err
		}
		sourceMetadata, err = loadProgramEventBlockLocaleMetadata(ctx, store, query.EventID, sourceState.SourceLocale)
		if err != nil {
			return err
		}
		blockMedia, err = store.MediaReferences(ctx, documentID)
		if err != nil {
			return err
		}
		blockMedia, err = store.HydrateBlockMedia(
			auth.WithUser(ctx, principal), query.EventID, documentID, principal, blockMedia,
		)
		return err
	}); err != nil {
		return RoomDocument{}, normalizeProgramEventContentBlockError(err)
	}
	return blockDocument(localeState, sourceMetadata, blockMedia, locale)
}

func blockDocument(
	localeState programEventExactLocaleState,
	sourceMetadata *BlockLocaleMetadata,
	blockMedia []*contentv1.ContentBlockMediaItem,
	locale string,
) (RoomDocument, error) {
	document, err := materializeProgramEventExactLocaleDocument(localeState, locale)
	if err != nil {
		return RoomDocument{}, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(localeState.Snapshot, locale)
	if err != nil {
		return RoomDocument{}, err
	}
	var localeMetadata *BlockLocaleMetadata
	if localeState.TargetMetadata != nil {
		localeMetadata = programEventLocaleMetadataProjection(sourceMetadata, localeState.TargetMetadata, locale)
	}
	var targetRevision *string
	if locale != localeState.SourceLocale && localeState.TargetMetadata != nil {
		targetRevision = &localeState.TargetRevision
	}
	return RoomDocument{
		Document: document, DocumentRevision: localeState.Snapshot.Document.Revision.String(),
		SourceMetadata: sourceMetadata, BlockMedia: blockMedia, Locale: locale,
		LocaleExists: localeState.TargetMetadata != nil, LocaleMetadata: localeMetadata,
		TargetRevision:      targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}, nil
}

// BlockWrite is the outcome of a collaboration write to one locale.
type BlockWrite struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	Locale           string
	// TargetRevision is set for a target locale.
	TargetRevision *string
}

// ApplyBlockBatchCommand applies a collaboration Block batch to one locale
// of the event's document.
type ApplyBlockBatchCommand struct {
	EventID                string
	Locale                 string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
	ExpectedTargetRevision *string
}

// ApplyBlockBatch stores a collaboration Block batch attributed to its one
// origin Member. A source locale batch may change the Block graph; a target
// locale batch changes only that locale's overlay.
func (s *Service) ApplyBlockBatch(ctx context.Context, command ApplyBlockBatchCommand) (BlockWrite, error) {
	if command.Batch == nil {
		return BlockWrite{}, domainerr.Required("batch")
	}
	locale, err := normalizeProgramEventDocumentLocale(command.Locale)
	if err != nil {
		return BlockWrite{}, err
	}
	documentID, err := s.contentDocumentID(ctx, command.EventID)
	if err != nil {
		return BlockWrite{}, err
	}
	batch, err := collaborationBlockBatch(documentID, locale, command)
	if err != nil {
		return BlockWrite{}, err
	}
	contributors := command.Batch.GetContributorMemberIds()
	if len(contributors) != 1 {
		return BlockWrite{}, domainerr.InvalidField(
			"contributor_member_ids", "collaboration mutation requires exactly one origin Member",
		)
	}
	sourceState, err := s.sourceLocale(ctx, command.EventID)
	if err != nil {
		return BlockWrite{}, err
	}
	write := collaborationWrite{
		eventID: command.EventID, documentID: documentID, locale: locale,
		contributors: contributors, fields: []string{"content"}, now: time.Now().UTC(),
	}
	if locale != sourceState.SourceLocale {
		return s.writeTargetLocale(ctx, write, programEventTargetLocaleMutationInput{
			Batch: batch, ExpectedDocumentRevision: batch.ExpectedRevision,
			ExpectedTargetRevision: command.ExpectedTargetRevision,
		})
	}
	if command.ExpectedTargetRevision != nil {
		return BlockWrite{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.writeSourceLocale(ctx, write, func(ctx context.Context, store Store, fence ContentFence) (contentblock.Result, error) {
		result, err := store.ApplyContentBatch(ctx, batch, fence)
		if err != nil || !result.Changed {
			return result, err
		}
		// A translation source change needs no extra write: the owning
		// Program Event root and content document revision are the complete
		// source authority. No separate translation epoch is stored.
		return result, store.TouchEvent(ctx, write.eventID, write.now)
	})
}

// contentDocumentID reads the event's content document in a snapshot of its
// own, before the write that locks it.
func (s *Service) contentDocumentID(ctx context.Context, eventID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		documentID, err = loadProgramEventContentDocumentID(ctx, store, eventID, NoLock)
		return err
	})
	return documentID, err
}

// sourceLocale reads the event's source locale in a snapshot of its own.
func (s *Service) sourceLocale(ctx context.Context, eventID string) (programEventSourceState, error) {
	var state programEventSourceState
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		state, err = loadProgramEventSourceLocale(ctx, store, eventID, false)
		return err
	})
	return state, err
}

// collaborationBlockBatch restores the typed batch of command for the room
// locale; every Block locale group must be that locale.
func collaborationBlockBatch(documentID uuid.UUID, locale string, command ApplyBlockBatchCommand) (contentblock.Batch, error) {
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		command.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return contentblock.Batch{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return contentblock.Batch{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, command.Batch.Profile, storage)
	if err != nil {
		return contentblock.Batch{}, normalizeProgramEventContentBlockError(err)
	}
	for index := range batch.LocaleGroups {
		normalized, normalizeErr := normalizeProgramEventDocumentLocale(batch.LocaleGroups[index].Locale)
		if normalizeErr != nil {
			return contentblock.Batch{}, domainerr.InvalidField("locale", "unsupported Block locale")
		}
		if normalized != locale {
			return contentblock.Batch{}, domainerr.InvalidField("locale", "Block mutation must match the authenticated room locale")
		}
		batch.LocaleGroups[index].Locale = normalized
	}
	return batch, nil
}

// UpdateLocaleMetadataCommand changes the title or summary of one locale
// from a collaboration session. The title is source-owned.
type UpdateLocaleMetadataCommand struct {
	EventID                string
	Locale                 string
	Title                  *string
	Summary                *entityparticipant.StringMutation
	ExpectedRevision       string
	ContributorMemberIDs   []string
	ExpectedTargetRevision *string
}

// UpdateLocaleMetadata stores a collaboration metadata edit under the
// document revision it was read at.
func (s *Service) UpdateLocaleMetadata(ctx context.Context, command UpdateLocaleMetadataCommand) (BlockWrite, error) {
	locale, err := normalizeProgramEventDocumentLocale(command.Locale)
	if err != nil {
		return BlockWrite{}, err
	}
	expectedRevision, err := uuidParseCanonical(command.ExpectedRevision, "expected_revision")
	if err != nil {
		return BlockWrite{}, err
	}
	patchTitle, patchSummary := command.Title != nil, command.Summary != nil
	if !patchTitle && !patchSummary {
		return BlockWrite{}, domainerr.InvalidField("metadata", "title or summary mutation is required")
	}
	if len(command.ContributorMemberIDs) != 1 {
		return BlockWrite{}, domainerr.InvalidField(
			"contributor_member_ids", "collaboration mutation requires exactly one origin Member",
		)
	}
	summary, err := programEventSummaryMutationValue(command.Summary)
	if err != nil {
		return BlockWrite{}, err
	}
	documentID, err := s.contentDocumentID(ctx, command.EventID)
	if err != nil {
		return BlockWrite{}, err
	}
	sourceState, err := s.sourceLocale(ctx, command.EventID)
	if err != nil {
		return BlockWrite{}, err
	}
	write := collaborationWrite{
		eventID: command.EventID, documentID: documentID, locale: locale,
		contributors: command.ContributorMemberIDs, now: time.Now().UTC(),
	}
	if locale != sourceState.SourceLocale {
		if patchTitle {
			return BlockWrite{}, domainerr.InvalidField("title", "Program Event title is source-owned")
		}
		contributors, err := contributorUUIDs(command.ContributorMemberIDs)
		if err != nil {
			return BlockWrite{}, err
		}
		write.fields = []string{"summary"}
		return s.writeTargetLocale(ctx, write, programEventTargetLocaleMutationInput{
			Batch: contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: contributors,
			},
			ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: command.ExpectedTargetRevision,
			SetSummary: patchSummary, Summary: summary,
		})
	}
	if command.ExpectedTargetRevision != nil {
		return BlockWrite{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.writeSourceMetadata(ctx, write, command, expectedRevision, summary)
}

// programEventSummaryMutationValue is the summary a mutation sets, nil when
// it clears it.
func programEventSummaryMutationValue(input *entityparticipant.StringMutation) (*string, error) {
	switch {
	case input == nil:
		return nil, nil
	case input.Set != nil:
		value := *input.Set
		return &value, nil
	case input.Clear != nil:
		if !*input.Clear {
			return nil, domainerr.InvalidField("summary", "clear must be true")
		}
		return nil, nil
	default:
		return nil, domainerr.InvalidField("summary", "set or clear is required")
	}
}

// writeSourceMetadata stores a source locale title or summary edit as a
// revision of the event's document.
func (s *Service) writeSourceMetadata(
	ctx context.Context,
	write collaborationWrite,
	command UpdateLocaleMetadataCommand,
	expectedRevision uuid.UUID,
	summary *string,
) (BlockWrite, error) {
	patchTitle, patchSummary := command.Title != nil, command.Summary != nil
	if patchTitle {
		write.fields = append(write.fields, "title")
	}
	if patchSummary {
		write.fields = append(write.fields, "summary")
	}
	var sourceLocale string
	written, err := s.writeSourceLocale(ctx, write, func(ctx context.Context, store Store, fence ContentFence) (contentblock.Result, error) {
		advanced, err := store.AdvanceContentRevision(
			ctx, contentblock.AdvanceInput{DocumentID: write.documentID, ExpectedRevision: expectedRevision}, fence,
			func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
				current, err := loadProgramEventSourceLocale(ctx, store, command.EventID, true)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				sourceLocale = current.SourceLocale
				changed, affectsSource, err := applyProgramEventLocaleMetadataMutation(
					ctx, store, command.EventID, sourceLocale, command.Title, patchTitle, summary, patchSummary, write.now,
				)
				return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: affectsSource}, err
			},
		)
		return contentblock.Result{
			DocumentRevision: advanced.DocumentRevision, Changed: advanced.Changed,
			TranslationSourceChanged: advanced.TranslationSourceChanged,
		}, err
	})
	written.ChangedLocales = []string{sourceLocale}
	return written, err
}

func contributorUUIDs(memberIDs []string) ([]uuid.UUID, error) {
	contributors := make([]uuid.UUID, len(memberIDs))
	for index, raw := range memberIDs {
		var err error
		contributors[index], err = uuidParseCanonical(raw, "contributor_member_ids")
		if err != nil {
			return nil, domainerr.InvalidField("contributor_member_ids", "must contain canonical Member UUIDs")
		}
	}
	return contributors, nil
}

// collaborationWrite is one collaboration write to a locale of an event's
// document, attributed to its single origin Member.
type collaborationWrite struct {
	eventID      string
	documentID   uuid.UUID
	locale       string
	contributors []string
	fields       []string
	now          time.Time
}

// collaborationSpec is the pipeline of w: the event root is locked and must
// still own the document before the contributors' current authority is
// re-resolved.
func (s *Service) collaborationSpec(w collaborationWrite, change func(context.Context, Store) (BlockWrite, error), signal func(BlockWrite) ContentUpdate, operation sharedtelemetry.AuditItemOperation) mutation.StoreSpec[BlockWrite, Store] {
	return mutation.StoreSpec[BlockWrite, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return lockOwningProgramEventRoot(ctx, store, w.eventID, w.documentID)
		},
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return store.RequireCurrentContributors(ctx, w.eventID, w.contributors)
		}),
		Change: change,
		Audit: func(written BlockWrite) mutation.Audit {
			if !written.Changed {
				return mutation.Unchanged()
			}
			return localeContentAudit(w.contributors[0], w.eventID, w.locale, operation)
		},
		Outbox: func(ctx context.Context, store Store, written BlockWrite) error {
			if !written.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, signal(written))
		},
	}
}

func (s *Service) writeTargetLocale(ctx context.Context, w collaborationWrite, input programEventTargetLocaleMutationInput) (BlockWrite, error) {
	input.EventID, input.DocumentID, input.Locale, input.Now = w.eventID, w.documentID, w.locale, w.now
	input.Fence = lockedContentFence(w.eventID, w.documentID)
	written, err := mutation.Run(ctx, s.mutations, s.collaborationSpec(w,
		func(ctx context.Context, store Store) (BlockWrite, error) {
			output, err := applyProgramEventTargetLocaleMutation(ctx, store, input)
			if err != nil {
				return BlockWrite{}, err
			}
			return BlockWrite{
				DocumentRevision: output.Result.DocumentRevision.String(), Changed: output.Result.Changed,
				ChangedLocales: output.Result.ChangedLocales, Locale: w.locale, TargetRevision: &output.TargetRevision,
			}, nil
		},
		func(written BlockWrite) ContentUpdate {
			return ContentUpdate{
				EventID: w.eventID, Fields: w.fields, DocumentRevision: written.DocumentRevision,
				Contributors: w.contributors, Source: SourceCollaboration,
				Locale: w.locale, TargetRevision: written.TargetRevision,
			}
		},
		programEventTargetLocaleContentOperation(false, false, true),
	))
	if err != nil {
		return BlockWrite{}, normalizeProgramEventContentBlockError(err)
	}
	return written, nil
}

func (s *Service) writeSourceLocale(
	ctx context.Context,
	w collaborationWrite,
	write func(context.Context, Store, ContentFence) (contentblock.Result, error),
) (BlockWrite, error) {
	written, err := mutation.Run(ctx, s.mutations, s.collaborationSpec(w,
		func(ctx context.Context, store Store) (BlockWrite, error) {
			result, err := write(ctx, store, lockedContentFence(w.eventID, w.documentID))
			if err != nil {
				return BlockWrite{}, normalizeProgramEventContentBlockError(err)
			}
			return BlockWrite{
				DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
				SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales, Locale: w.locale,
			}, nil
		},
		func(written BlockWrite) ContentUpdate {
			return ContentUpdate{
				EventID: w.eventID, Fields: w.fields, DocumentRevision: written.DocumentRevision,
				Contributors: w.contributors, Source: SourceCollaboration,
				Locale: w.locale, DocumentStateChanged: true,
			}
		},
		sharedtelemetry.AuditItemOperationUpdated,
	))
	if err != nil {
		return BlockWrite{}, normalizeProgramEventContentBlockError(err)
	}
	return written, nil
}

// lockOwningProgramEventRoot locks the event row and requires it to still
// own documentID: the event can be deleted or its document replaced while a
// collaboration document is open.
func lockOwningProgramEventRoot(ctx context.Context, store Store, eventID string, documentID uuid.UUID) error {
	reference, err := store.ContentDocumentID(ctx, eventID, UpdateLock)
	if err != nil {
		return notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
	}
	if reference == nil || *reference != documentID.String() {
		return domainerr.Precondition("Program Event content document ownership changed; reload before saving")
	}
	return nil
}

// lockedContentFence is the content document fence of a mutation that
// already locked the owning root of documentID and authorized the caller.
func lockedContentFence(eventID string, documentID uuid.UUID) ContentFence {
	return func(ctx context.Context, store Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Program Event content document ownership changed; reload before saving")
		}
		sourceState, err := loadProgramEventSourceLocale(ctx, store, eventID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceState.SourceLocale}, nil
	}
}

// localeContentAudit attributes a locale content change to the Member who
// made it.
func localeContentAudit(memberID, eventID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventLocaleContentAuditRecord(metadata, eventID, locale, operation)
	})
}

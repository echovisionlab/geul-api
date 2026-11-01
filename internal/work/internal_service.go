package work

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// LoadWorkBlockDocumentQuery loads one collaboration room for the session
// editor-collab admitted.
type LoadWorkBlockDocumentQuery struct {
	WorkID    string
	Locale    string
	SessionID string
}

// ApplyWorkBlockBatchCommand saves one collaboration room's Block batch.
type ApplyWorkBlockBatchCommand struct {
	WorkID                 string
	Locale                 string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
	ExpectedTargetRevision *string
}

// UpdateWorkLocaleMetadataCommand saves one room's title or summary. A nil
// Title keeps it; SetSummary replaces the summary with Summary, nil clearing
// it.
type UpdateWorkLocaleMetadataCommand struct {
	WorkID                 string
	Locale                 string
	Title                  *string
	SetSummary             bool
	Summary                *string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	ContributorMemberIDs   []string
}

// CreateWorkVersionCheckpointCommand records the source room as a version
// when it differs from the latest one.
type CreateWorkVersionCheckpointCommand struct {
	WorkID               string
	Locale               string
	ExpectedRevision     string
	ContributorMemberIDs []string
}

// RoomMetadata is the title and summary of one locale a room shows.
type RoomMetadata struct {
	Locale  string
	Title   *string
	Summary *string
}

// RoomDocument is a collaboration room's Work document with its locale
// metadata.
type RoomDocument struct {
	Document            *contentv1.LocalizedRichTextDocument
	DocumentRevision    string
	SourceMetadata      RoomMetadata
	LocaleMetadata      *RoomMetadata
	BlockMedia          []*contentv1.ContentBlockMediaItem
	Locale              string
	LocaleExists        bool
	TargetRevision      *string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, Store) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: access,
		Change: func(ctx context.Context, store Store) (struct{}, error) { return struct{}{}, read(ctx, store) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func (s *Service) LoadWorkBlockDocument(ctx context.Context, query LoadWorkBlockDocumentQuery) (RoomDocument, error) {
	if _, err := parseWorkContentUUID("work_id", query.WorkID); err != nil {
		return RoomDocument{}, err
	}
	locale, err := normalizeWorkDocumentLocale(query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	documentID, err := s.readWorkContentDocumentID(ctx, query.WorkID)
	if err != nil {
		return RoomDocument{}, err
	}
	var principal *auth.UserInfo
	var state workTargetLocaleState
	var mediaItems []*contentv1.ContentBlockMediaItem
	// editor-collab reads as itself on behalf of the admitted session.
	access := mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		var err error
		principal, err = requireWorkLoadPrincipal(ctx, store, lockActive, s.spiceDB, query.WorkID, documentID, query.SessionID)
		return err
	})
	err = s.readLocked(ctx, access, func(ctx context.Context, store Store) error {
		var err error
		if state, err = loadWorkTargetLocaleState(ctx, store, query.WorkID, documentID, locale, false); err != nil {
			return err
		}
		if mediaItems, err = store.MediaReferences(ctx, documentID); err != nil {
			return err
		}
		mediaItems, err = store.HydrateBlockMedia(auth.WithUser(ctx, principal), query.WorkID, documentID, principal, mediaItems)
		return err
	})
	if err != nil {
		return RoomDocument{}, normalizeWorkContentBlockError(err)
	}
	if state.SourceMetadata.Title == nil || *state.SourceMetadata.Title == "" {
		return RoomDocument{}, domainerr.Precondition("Work source title is not initialized")
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return RoomDocument{}, normalizeWorkContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.Snapshot, locale)
	if err != nil {
		return RoomDocument{}, err
	}
	room := RoomDocument{
		Document:         document,
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: RoomMetadata{
			Locale: state.SourceLocale, Title: state.SourceMetadata.Title, Summary: state.SourceMetadata.Summary,
		},
		BlockMedia:   mediaItems,
		Locale:       locale,
		LocaleExists: locale == state.SourceLocale || state.TargetMetadata != nil,
	}
	if locale == state.SourceLocale {
		source := room.SourceMetadata
		room.LocaleMetadata = &source
	} else if state.TargetMetadata != nil {
		projected := workTargetMetadataProjection(state.SourceMetadata, state.TargetMetadata, locale)
		room.LocaleMetadata = &RoomMetadata{Locale: projected.Locale, Title: projected.Title, Summary: projected.Summary}
		room.TargetRevision = &state.TargetRevision
	}
	room.PresentLocaleValues = presentLocaleValues
	return room, nil
}

// RoomSave is the outcome of one collaboration save.
type RoomSave struct {
	Locale         string
	SourceLocale   string
	Content        contentblock.Result
	TargetRevision *string
	fields         []string
}

// collaborationSave runs a collaboration save of workID's room: editor-collab
// calls as itself, so the room is locked and its contributors' current
// permission authorizes the save. change receives the authorized content
// fence.
func (s *Service) collaborationSave(
	ctx context.Context,
	workID string,
	documentID uuid.UUID,
	contributors []string,
	change func(context.Context, Store, contentblock.DomainContext, ContentFence) (RoomSave, error),
) (RoomSave, error) {
	contributorMemberID, err := contentblock.MutationContributor(contributors)
	if err != nil {
		return RoomSave{}, err
	}
	var domain contentblock.DomainContext
	saved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[RoomSave, Store]{
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			domain, err = internalWorkContentFence(contributors)(ctx, store, documentID)
			return err
		}),
		Change: func(ctx context.Context, store Store) (RoomSave, error) {
			saved, err := change(ctx, store, domain, lockedContentFence(documentID, domain))
			saved.SourceLocale = domain.SourceLocale
			return saved, err
		},
		Audit: func(saved RoomSave) mutation.Audit {
			if !saved.Content.Changed {
				return mutation.Unchanged()
			}
			return workLocaleContentAudit(contributorMemberID, workID, saved.Locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, store Store, saved RoomSave) error {
			if !saved.Content.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{WorkID: workID, Kind: LocaleUpdate, Locale: LocaleChange{
				Fields:           saved.fields,
				DocumentRevision: saved.Content.DocumentRevision.String(), Contributors: contributors,
				Source: SourceCollaboration,
				Locale: saved.Locale, TargetRevision: saved.TargetRevision,
				DocumentStateChanged: saved.Locale == saved.SourceLocale,
			}})
		},
	})
	if err != nil {
		return RoomSave{}, normalizeWorkContentBlockError(err)
	}
	return saved, nil
}

func (s *Service) ApplyWorkBlockBatch(ctx context.Context, command ApplyWorkBlockBatchCommand) (RoomSave, error) {
	if command.Batch == nil {
		return RoomSave{}, domainerr.Required("batch")
	}
	if _, err := parseWorkContentUUID("work_id", command.WorkID); err != nil {
		return RoomSave{}, err
	}
	locale, err := normalizeWorkDocumentLocale(command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	if _, err := contentblock.MutationContributor(command.Batch.ContributorMemberIds); err != nil {
		return RoomSave{}, err
	}
	documentID, err := s.readWorkContentDocumentID(ctx, command.WorkID)
	if err != nil {
		return RoomSave{}, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		command.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return RoomSave{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return RoomSave{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, command.Batch.Profile, storage)
	if err != nil {
		return RoomSave{}, normalizeWorkContentBlockError(err)
	}
	now := time.Now().UTC()
	return s.collaborationSave(ctx, command.WorkID, documentID, command.Batch.ContributorMemberIds,
		func(ctx context.Context, store Store, domain contentblock.DomainContext, fence ContentFence) (RoomSave, error) {
			saved := RoomSave{Locale: locale, fields: []string{"content"}}
			if locale != domain.SourceLocale {
				var err error
				saved.Content, saved.TargetRevision, err = applyWorkTargetLocaleMutation(ctx, store, workTargetLocaleMutationInput{
					WorkID: command.WorkID, DocumentID: documentID, Locale: locale, Batch: batch,
					ExpectedTargetRevision: command.ExpectedTargetRevision, Now: now, Fence: fence,
				})
				return saved, err
			}
			if command.ExpectedTargetRevision != nil {
				return saved, domainerr.InvalidField("expected_target_revision", "source Work room cannot carry a target revision")
			}
			if err := validateWorkSourceStorage(storage, domain.SourceLocale); err != nil {
				return saved, err
			}
			result, err := store.ApplyContentBatch(ctx, batch, fence)
			if err != nil {
				return saved, err
			}
			saved.Content = result
			for _, changedLocale := range result.ChangedLocales {
				if changedLocale != domain.SourceLocale {
					return saved, domainerr.Precondition("target Work locale changed while saving source content")
				}
			}
			if result.Changed {
				return saved, store.TouchWork(ctx, command.WorkID, now)
			}
			return saved, nil
		},
	)
}

// UpdateWorkLocaleMetadata saves one room's title or summary.
func (s *Service) UpdateWorkLocaleMetadata(ctx context.Context, command UpdateWorkLocaleMetadataCommand) (RoomSave, error) {
	documentID, err := s.readWorkContentDocumentID(ctx, command.WorkID)
	if err != nil {
		return RoomSave{}, err
	}
	expectedRevision, err := parseWorkContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return RoomSave{}, err
	}
	contributors, err := workContributorUUIDs(command.ContributorMemberIDs)
	if err != nil {
		return RoomSave{}, err
	}
	if _, err := contentblock.MutationContributor(command.ContributorMemberIDs); err != nil {
		return RoomSave{}, err
	}
	now := time.Now().UTC()
	return s.collaborationSave(ctx, command.WorkID, documentID, command.ContributorMemberIDs,
		func(ctx context.Context, store Store, domain contentblock.DomainContext, fence ContentFence) (RoomSave, error) {
			plan, err := planWorkLocaleMetadataMutation(command, domain.SourceLocale)
			if err != nil {
				return RoomSave{}, err
			}
			if plan.Locale != domain.SourceLocale {
				return saveWorkTargetLocaleMetadata(ctx, store, command, plan, contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: contributors,
				}, now, fence)
			}
			if command.ExpectedTargetRevision != nil {
				return RoomSave{}, domainerr.InvalidField("expected_target_revision", "source Work metadata cannot carry a target revision")
			}
			return saveWorkSourceLocaleMetadata(ctx, store, command.WorkID, plan, documentID, expectedRevision, now, fence)
		},
	)
}

func saveWorkTargetLocaleMetadata(
	ctx context.Context,
	store Store,
	command UpdateWorkLocaleMetadataCommand,
	plan workLocaleMetadataMutationPlan,
	batch contentblock.Batch,
	now time.Time,
	fence ContentFence,
) (RoomSave, error) {
	result, targetRevision, err := applyWorkTargetLocaleMutation(ctx, store, workTargetLocaleMutationInput{
		WorkID: command.WorkID, DocumentID: batch.DocumentID, Locale: plan.Locale, Batch: batch,
		ExpectedTargetRevision: command.ExpectedTargetRevision,
		Patch: workTargetMetadataPatch{
			UpdateTitle: plan.UpdateTitle, Title: plan.Title,
			UpdateSummary: plan.UpdateSummary, Summary: plan.Summary,
		},
		Now: now, Fence: fence,
	})
	saved := RoomSave{Locale: plan.Locale, Content: result, TargetRevision: targetRevision}
	if err != nil || !result.Changed {
		return saved, err
	}
	saved.fields = workLocaleMetadataMutationResult{TitleChanged: plan.UpdateTitle, SummaryChanged: plan.UpdateSummary}.changedFields()
	if plan.UpdateTitle {
		_, err = requestWorkOG(ctx, store, command.WorkID, plan.Locale, "work_target_locale_metadata_saved")
	}
	return saved, err
}

func saveWorkSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	workID string,
	plan workLocaleMetadataMutationPlan,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	now time.Time,
	fence ContentFence,
) (RoomSave, error) {
	var mutation workLocaleMetadataMutationResult
	advance, err := store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		workSourceRoomFence(plan.Locale, fence),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			var err error
			mutation, err = applyWorkLocaleMetadataMutation(ctx, store, workID, plan, now)
			return mutation.Effect, err
		},
	)
	if err != nil {
		return RoomSave{}, err
	}
	saved := RoomSave{
		Locale: plan.Locale,
		Content: contentblock.Result{
			DocumentRevision: advance.DocumentRevision, Changed: advance.Changed,
			TranslationSourceChanged: advance.TranslationSourceChanged,
		},
		fields: mutation.changedFields(),
	}
	if !advance.Changed {
		return saved, nil
	}
	if err := store.TouchWork(ctx, workID, now); err != nil {
		return saved, err
	}
	if advance.TranslationSourceChanged {
		_, err = requestWorkOG(ctx, store, workID, plan.Locale, "work_locale_metadata_saved")
	}
	return saved, err
}

// requestWorkOG requests the current OG image of workID's locale and
// returns its run, if one started.
func requestWorkOG(ctx context.Context, store Store, workID, locale, reason string) (*string, error) {
	runID, err := store.RequestOG(ctx, workID, locale, reason)
	if runID == "" {
		return nil, err
	}
	return &runID, err
}

// VersionCheckpoint is the outcome of a checkpoint request: the version it
// created, if any, and the document revision.
type VersionCheckpoint struct {
	Created  *Version
	Revision string
	Locale   string
}

func (s *Service) CreateWorkVersionCheckpoint(ctx context.Context, command CreateWorkVersionCheckpointCommand) (VersionCheckpoint, error) {
	locale, err := normalizeWorkDocumentLocale(command.Locale)
	if err != nil {
		return VersionCheckpoint{}, err
	}
	documentID, err := s.readWorkContentDocumentID(ctx, command.WorkID)
	if err != nil {
		return VersionCheckpoint{}, err
	}
	expectedRevision, err := parseWorkContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return VersionCheckpoint{}, err
	}
	contributors := normalizeContributorMemberIDs(command.ContributorMemberIDs)
	if !slices.Equal(contributors, command.ContributorMemberIDs) {
		return VersionCheckpoint{}, domainerr.InvalidField("contributor_member_ids", "must be sorted and unique")
	}
	var advance contentblock.AdvanceResult
	checkpoint, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[VersionCheckpoint, Store]{
		// The checkpoint fence locks the room and authorizes its
		// contributors while it advances the document revision.
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			advance, err = store.AdvanceContentRevision(
				ctx,
				contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
				internalWorkContentFence(command.ContributorMemberIDs),
				func(context.Context, Store) (contentblock.MetadataEffect, error) {
					return contentblock.MetadataEffect{}, nil
				},
			)
			return err
		}),
		Change: func(ctx context.Context, store Store) (VersionCheckpoint, error) {
			created, err := createWorkVersion(ctx, store, command.WorkID, locale, documentID, contributors)
			return VersionCheckpoint{Created: created, Revision: advance.DocumentRevision.String(), Locale: locale}, err
		},
		Audit: func(checkpoint VersionCheckpoint) mutation.Audit {
			if checkpoint.Created == nil {
				return mutation.Unchanged()
			}
			created := checkpoint.Created
			return mutation.VersionAudit(sharedtelemetry.AuditWorkUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkVersionCreatedAuditRecord(metadata, command.WorkID, created.ID, created.ContributorMemberIDs)
			})
		},
	})
	if err != nil {
		return VersionCheckpoint{}, normalizeWorkContentBlockError(err)
	}
	return checkpoint, nil
}

// createWorkVersion records the source locale as the next version, or
// returns nil when it equals the latest version.
func createWorkVersion(
	ctx context.Context,
	store Store,
	workID, locale string,
	documentID uuid.UUID,
	contributors []string,
) (*Version, error) {
	source, err := LoadRequiredSourceLocaleMetadata(ctx, store, workID)
	if err != nil {
		return nil, err
	}
	if locale != source.Locale {
		return nil, domainerr.InvalidField("locale", "Work version checkpoints are source-locale only")
	}
	if source.Title == nil || *source.Title == "" {
		return nil, domainerr.Precondition("Work source title is not initialized")
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, source.Locale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, source.Locale)
	if err != nil {
		return nil, err
	}
	encoded, err := EncodeVersionContentSnapshot(source.Locale, source.Title, source.Summary, document)
	if err != nil {
		return nil, err
	}
	previous, exists, err := store.LockLatestVersion(ctx, workID)
	if err != nil {
		return nil, err
	}
	nextVersion := int32(1)
	if exists {
		same, err := sameWorkVersionContentSnapshot(previous.ContentSnapshot, encoded)
		if err != nil || same {
			return nil, err
		}
		nextVersion = previous.Version + 1
	}
	created := &Version{
		WorkID:               workID,
		Version:              nextVersion,
		Title:                cloneOptionalString(source.Title),
		Summary:              cloneOptionalString(source.Summary),
		ContentSnapshot:      encoded,
		ContributorMemberIDs: contributors,
		CreatedAt:            time.Now().UTC(),
	}
	if err := store.InsertVersion(ctx, created); err != nil {
		return nil, err
	}
	return created, nil
}

func sameWorkVersionContentSnapshot(current []byte, desired []byte) (bool, error) {
	// A checkpoint from an earlier block catalog cannot equal current content;
	// keep it as history rather than decoding it against today's catalog.
	if len(current) == 0 || staleCatalogSnapshot(current) {
		return false, nil
	}
	currentEnvelope, currentDocument, err := DecodeVersionContentSnapshot(current)
	if err != nil {
		return false, fmt.Errorf("decode previous Work version snapshot: %w", err)
	}
	desiredEnvelope, desiredDocument, err := DecodeVersionContentSnapshot(desired)
	if err != nil {
		return false, fmt.Errorf("decode desired Work version snapshot: %w", err)
	}
	return currentEnvelope.SchemaVersion == desiredEnvelope.SchemaVersion &&
		currentEnvelope.SourceLocale == desiredEnvelope.SourceLocale &&
		nullableStringEqual(currentEnvelope.Title, desiredEnvelope.Title) &&
		nullableStringEqual(currentEnvelope.Summary, desiredEnvelope.Summary) &&
		proto.Equal(currentDocument, desiredDocument), nil
}

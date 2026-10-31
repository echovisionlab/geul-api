package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
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

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, *gorm.DB) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, read(ctx, tx) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func (s *Service) LoadWorkBlockDocument(ctx context.Context, query LoadWorkBlockDocumentQuery) (*intrav1.LoadWorkBlockDocumentResponse, error) {
	if _, err := parseWorkContentUUID("work_id", query.WorkID); err != nil {
		return nil, err
	}
	locale, err := normalizeWorkDocumentLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, query.WorkID)
	if err != nil {
		return nil, err
	}
	var principal *auth.UserInfo
	var state workTargetLocaleState
	var mediaItems []*contentv1.ContentBlockMediaItem
	// editor-collab reads as itself on behalf of the admitted session.
	access := mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		var err error
		principal, err = requireWorkLoadPrincipal(ctx, tx, s.spiceDB, query.WorkID, documentID, query.SessionID)
		return err
	})
	err = s.readLocked(ctx, access, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		if state, err = loadWorkTargetLocaleState(ctx, tx, s.contentBlocks, query.WorkID, documentID, locale, false); err != nil {
			return err
		}
		if mediaItems, err = contentblock.LoadMediaReferences(ctx, tx, documentID); err != nil {
			return err
		}
		mediaItems, err = s.mediaHydrator.HydrateAuthorizedWorkBlockMediaWithDB(
			auth.WithUser(ctx, principal), tx, query.WorkID, documentID, principal, mediaItems,
		)
		return err
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	if state.SourceMetadata.Title == nil || *state.SourceMetadata.Title == "" {
		return nil, domainerr.Precondition("Work source title is not initialized")
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.Snapshot, locale)
	if err != nil {
		return nil, err
	}
	response := &intrav1.LoadWorkBlockDocumentResponse{
		Document:         document,
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.WorkLocaleMetadata{
			Locale: state.SourceLocale, Title: state.SourceMetadata.Title, Summary: state.SourceMetadata.Summary,
		},
		BlockMedia:   mediaItems,
		Locale:       locale,
		LocaleExists: locale == state.SourceLocale || state.TargetMetadata != nil,
	}
	if locale == state.SourceLocale {
		response.LocaleMetadata = response.SourceMetadata
	} else if state.TargetMetadata != nil {
		projected := workTargetMetadataProjection(state.SourceMetadata, state.TargetMetadata, locale)
		response.LocaleMetadata = &intrav1.WorkLocaleMetadata{
			Locale: projected.Locale, Title: projected.Title, Summary: projected.Summary,
		}
		response.TargetRevision = &state.TargetRevision
	}
	response.PresentLocaleValues = presentLocaleValues
	return response, nil
}

// workRoomSave is the outcome of one collaboration save.
type workRoomSave struct {
	locale         string
	sourceLocale   string
	content        contentblock.Result
	targetRevision *string
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
	change func(context.Context, *gorm.DB, contentblock.DomainContext, contentblock.DomainFence) (workRoomSave, error),
) (workRoomSave, error) {
	contributorMemberID, err := contentblock.MutationContributor(contributors)
	if err != nil {
		return workRoomSave{}, err
	}
	var domain contentblock.DomainContext
	saved, err := mutation.Run(ctx, s.mutations, mutation.Spec[workRoomSave]{
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			domain, err = internalWorkContentFence(s.checkpoints, contributors)(ctx, tx, documentID)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (workRoomSave, error) {
			saved, err := change(ctx, tx, domain, contentblock.LockedFence(workEntityName, documentID, domain))
			saved.sourceLocale = domain.SourceLocale
			return saved, err
		},
		Audit: func(saved workRoomSave) mutation.Audit {
			if !saved.content.Changed {
				return mutation.Unchanged()
			}
			return workLocaleContentAudit(contributorMemberID, workID, saved.locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, saved workRoomSave) error {
			if !saved.content.Changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.signals, buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID: workID, Fields: saved.fields,
				DocumentRevision: saved.content.DocumentRevision.String(), Contributors: contributors,
				Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale: saved.locale, TargetRevision: saved.targetRevision,
				DocumentStateChanged: saved.locale == saved.sourceLocale,
			}))
		},
	})
	if err != nil {
		return workRoomSave{}, normalizeWorkContentBlockError(err)
	}
	return saved, nil
}

func (s *Service) ApplyWorkBlockBatch(ctx context.Context, command ApplyWorkBlockBatchCommand) (*intrav1.ApplyWorkBlockBatchResponse, error) {
	if command.Batch == nil {
		return nil, domainerr.Required("batch")
	}
	if _, err := parseWorkContentUUID("work_id", command.WorkID); err != nil {
		return nil, err
	}
	locale, err := normalizeWorkDocumentLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := contentblock.MutationContributor(command.Batch.ContributorMemberIds); err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, command.WorkID)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		command.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return nil, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, command.Batch.Profile, storage)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	now := time.Now().UTC()
	saved, err := s.collaborationSave(ctx, command.WorkID, documentID, command.Batch.ContributorMemberIds,
		func(ctx context.Context, tx *gorm.DB, domain contentblock.DomainContext, fence contentblock.DomainFence) (workRoomSave, error) {
			saved := workRoomSave{locale: locale, fields: []string{"content"}}
			if locale != domain.SourceLocale {
				var err error
				saved.content, saved.targetRevision, err = applyWorkTargetLocaleMutation(
					ctx, tx, s.contentBlocks, workTargetLocaleMutationInput{
						WorkID: command.WorkID, DocumentID: documentID, Locale: locale, Batch: batch,
						ExpectedTargetRevision: command.ExpectedTargetRevision, Now: now, Fence: fence,
					},
				)
				return saved, err
			}
			if command.ExpectedTargetRevision != nil {
				return saved, domainerr.InvalidField("expected_target_revision", "source Work room cannot carry a target revision")
			}
			if err := validateWorkSourceStorage(storage, domain.SourceLocale); err != nil {
				return saved, err
			}
			result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, fence)
			if err != nil {
				return saved, err
			}
			saved.content = result
			for _, changedLocale := range result.ChangedLocales {
				if changedLocale != domain.SourceLocale {
					return saved, domainerr.Precondition("target Work locale changed while saving source content")
				}
			}
			if result.Changed {
				return saved, touchWork(ctx, tx, command.WorkID, now)
			}
			return saved, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return &intrav1.ApplyWorkBlockBatchResponse{
		DocumentRevision: saved.content.DocumentRevision.String(), Changed: saved.content.Changed,
		SourceChanged:  saved.content.TranslationSourceChanged,
		ChangedLocales: saved.content.ChangedLocales,
		Locale:         locale, TargetRevision: saved.targetRevision,
	}, nil
}

// touchWork records a content change on the Work root.
func touchWork(ctx context.Context, tx *gorm.DB, workID string, now time.Time) error {
	return tx.WithContext(ctx).Model(&model.Work{}).Where("id = ?", workID).UpdateColumn("updated_at", now).Error
}

func (s *Service) UpdateWorkLocaleMetadata(ctx context.Context, command UpdateWorkLocaleMetadataCommand) (*intrav1.UpdateWorkLocaleMetadataResponse, error) {
	documentID, err := loadWorkContentDocumentID(ctx, s.db, command.WorkID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parseWorkContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	contributors, err := workContributorUUIDs(command.ContributorMemberIDs)
	if err != nil {
		return nil, err
	}
	if _, err := contentblock.MutationContributor(command.ContributorMemberIDs); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	saved, err := s.collaborationSave(ctx, command.WorkID, documentID, command.ContributorMemberIDs,
		func(ctx context.Context, tx *gorm.DB, domain contentblock.DomainContext, fence contentblock.DomainFence) (workRoomSave, error) {
			plan, err := planWorkLocaleMetadataMutation(command, domain.SourceLocale)
			if err != nil {
				return workRoomSave{}, err
			}
			if plan.Locale != domain.SourceLocale {
				return s.saveWorkTargetLocaleMetadata(ctx, tx, command, plan, contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: contributors,
				}, now, fence)
			}
			if command.ExpectedTargetRevision != nil {
				return workRoomSave{}, domainerr.InvalidField("expected_target_revision", "source Work metadata cannot carry a target revision")
			}
			return s.saveWorkSourceLocaleMetadata(ctx, tx, command.WorkID, plan, documentID, expectedRevision, now, fence)
		},
	)
	if err != nil {
		return nil, err
	}
	return &intrav1.UpdateWorkLocaleMetadataResponse{
		DocumentRevision: saved.content.DocumentRevision.String(), Changed: saved.content.Changed,
		SourceChanged: saved.locale == saved.sourceLocale && saved.content.TranslationSourceChanged, ChangedLocales: []string{saved.locale},
		Locale: saved.locale, TargetRevision: saved.targetRevision,
	}, nil
}

func (s *Service) saveWorkTargetLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	command UpdateWorkLocaleMetadataCommand,
	plan workLocaleMetadataMutationPlan,
	batch contentblock.Batch,
	now time.Time,
	fence contentblock.DomainFence,
) (workRoomSave, error) {
	result, targetRevision, err := applyWorkTargetLocaleMutation(
		ctx, tx, s.contentBlocks, workTargetLocaleMutationInput{
			WorkID: command.WorkID, DocumentID: batch.DocumentID, Locale: plan.Locale, Batch: batch,
			ExpectedTargetRevision: command.ExpectedTargetRevision,
			Patch: workTargetMetadataPatch{
				UpdateTitle: plan.UpdateTitle, Title: plan.Title,
				UpdateSummary: plan.UpdateSummary, Summary: plan.Summary,
			},
			Now: now, Fence: fence,
		},
	)
	saved := workRoomSave{locale: plan.Locale, content: result, targetRevision: targetRevision}
	if err != nil || !result.Changed {
		return saved, err
	}
	saved.fields = workLocaleMetadataMutationResult{TitleChanged: plan.UpdateTitle, SummaryChanged: plan.UpdateSummary}.changedFields()
	if plan.UpdateTitle {
		_, err = s.requestWorkOG(ctx, tx, command.WorkID, plan.Locale, "work_target_locale_metadata_saved")
	}
	return saved, err
}

func (s *Service) saveWorkSourceLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	plan workLocaleMetadataMutationPlan,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	now time.Time,
	fence contentblock.DomainFence,
) (workRoomSave, error) {
	var mutation workLocaleMetadataMutationResult
	advance, err := s.contentBlocks.AdvanceRevision(
		ctx, tx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		workSourceRoomFence(plan.Locale, fence),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			var err error
			mutation, err = applyWorkLocaleMetadataMutation(ctx, tx, workID, plan, now)
			return mutation.Effect, err
		},
	)
	if err != nil {
		return workRoomSave{}, err
	}
	saved := workRoomSave{
		locale: plan.Locale,
		content: contentblock.Result{
			DocumentRevision: advance.DocumentRevision, Changed: advance.Changed,
			TranslationSourceChanged: advance.TranslationSourceChanged,
		},
		fields: mutation.changedFields(),
	}
	if !advance.Changed {
		return saved, nil
	}
	if err := touchWork(ctx, tx, workID, now); err != nil {
		return saved, err
	}
	if advance.TranslationSourceChanged {
		_, err = s.requestWorkOG(ctx, tx, workID, plan.Locale, "work_locale_metadata_saved")
	}
	return saved, err
}

// requestWorkOG requests the current OG image of workID's locale and
// returns its run, if one started.
func (s *Service) requestWorkOG(ctx context.Context, tx *gorm.DB, workID, locale, reason string) (*string, error) {
	runID, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, locale, false, reason)
	if runID == "" {
		return nil, err
	}
	return &runID, err
}

// workVersionCheckpoint is the outcome of a checkpoint request.
type workVersionCheckpoint struct {
	created  *model.WorkVersion
	revision string
}

func (s *Service) CreateWorkVersionCheckpoint(ctx context.Context, command CreateWorkVersionCheckpointCommand) (*intrav1.CreateWorkVersionCheckpointResponse, error) {
	locale, err := normalizeWorkDocumentLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, command.WorkID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parseWorkContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	contributors := normalizeContributorMemberIDs(command.ContributorMemberIDs)
	if !slices.Equal([]string(contributors), command.ContributorMemberIDs) {
		return nil, domainerr.InvalidField("contributor_member_ids", "must be sorted and unique")
	}
	var advance contentblock.AdvanceResult
	checkpoint, err := mutation.Run(ctx, s.mutations, mutation.Spec[workVersionCheckpoint]{
		// The checkpoint fence locks the room and authorizes its
		// contributors while it advances the document revision.
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			advance, err = s.contentBlocks.AdvanceRevision(
				ctx, tx,
				contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
				internalWorkContentFence(s.checkpoints, command.ContributorMemberIDs),
				func(context.Context, *gorm.DB) (contentblock.MetadataEffect, error) {
					return contentblock.MetadataEffect{}, nil
				},
			)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (workVersionCheckpoint, error) {
			created, err := s.createWorkVersion(ctx, tx, command.WorkID, locale, documentID, contributors)
			return workVersionCheckpoint{created: created, revision: advance.DocumentRevision.String()}, err
		},
		Audit: func(checkpoint workVersionCheckpoint) mutation.Audit {
			if checkpoint.created == nil {
				return mutation.Unchanged()
			}
			created := checkpoint.created
			return mutation.VersionAudit(sharedtelemetry.AuditWorkUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkVersionCreatedAuditRecord(metadata, command.WorkID, created.ID, created.ContributorMemberIDs)
			})
		},
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	response := &intrav1.CreateWorkVersionCheckpointResponse{
		Created:  checkpoint.created != nil,
		Revision: checkpoint.revision,
		Locale:   locale,
	}
	if checkpoint.created != nil {
		response.VersionId = &checkpoint.created.ID
	}
	return response, nil
}

// createWorkVersion records the source locale as the next version, or
// returns nil when it equals the latest version.
func (s *Service) createWorkVersion(
	ctx context.Context,
	tx *gorm.DB,
	workID, locale string,
	documentID uuid.UUID,
	contributors []string,
) (*model.WorkVersion, error) {
	source, err := LoadRequiredSourceLocaleMetadata(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	if locale != source.Locale {
		return nil, domainerr.InvalidField("locale", "Work version checkpoints are source-locale only")
	}
	if source.Title == nil || *source.Title == "" {
		return nil, domainerr.Precondition("Work source title is not initialized")
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, source.Locale)
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
	var previous model.WorkVersion
	previousResult := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("work_id = ?", workID).
		Order("version DESC").
		First(&previous)
	if previousResult.Error != nil && !errors.Is(previousResult.Error, gorm.ErrRecordNotFound) {
		return nil, previousResult.Error
	}
	nextVersion := int32(1)
	if previousResult.Error == nil {
		same, err := sameWorkVersionContentSnapshot(previous.ContentSnapshot, encoded)
		if err != nil || same {
			return nil, err
		}
		nextVersion = previous.Version + 1
	}
	created := &model.WorkVersion{
		WorkID:               workID,
		Version:              nextVersion,
		Title:                cloneOptionalString(source.Title),
		Summary:              cloneOptionalString(source.Summary),
		ContentSnapshot:      encoded,
		ContributorMemberIDs: contributors,
		CreatedAt:            time.Now().UTC(),
	}
	if err := tx.WithContext(ctx).Create(created).Error; err != nil {
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

package release

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InternalReleaseService is the typed Block boundary used by the collaboration runtime.
type InternalReleaseService struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	checkpoints    persistencecheckpoint.ContributorFence
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	ogRefresher    ContentOG
	artists        ArtistSummaryLoader
	asyncPublisher AsyncPublisher
}

type InternalReleaseServiceOption func(*InternalReleaseService)

func WithInternalReleaseContentBlockStore(store *contentblock.Store) InternalReleaseServiceOption {
	return func(service *InternalReleaseService) { service.contentBlocks = store }
}

func WithInternalReleaseCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalReleaseServiceOption {
	return func(service *InternalReleaseService) { service.checkpoints = checkpoints }
}

// WithInternalReleaseAIDocumentPublisher reuses Release's existing product
// signal publisher for post-commit DCDP collaboration notifications.
func WithInternalReleaseAIDocumentPublisher(publisher AsyncPublisher) InternalReleaseServiceOption {
	return func(service *InternalReleaseService) { service.asyncPublisher = publisher }
}

func NewAuditedInternalReleaseService(db *gorm.DB, spiceDB *auth.SpiceDBClient, auditWriter domainaudit.Appender, ogRefresher ContentOG, artists ArtistSummaryLoader, options ...InternalReleaseServiceOption) *InternalReleaseService {
	if auditWriter == nil {
		panic("InternalReleaseService: audit writer is required")
	}
	service := NewInternalReleaseService(db, spiceDB, ogRefresher, artists, options...)
	service.auditWriter = auditWriter
	return service
}

func NewInternalReleaseService(db *gorm.DB, spiceDB *auth.SpiceDBClient, ogRefresher ContentOG, artists ArtistSummaryLoader, options ...InternalReleaseServiceOption) *InternalReleaseService {
	if db == nil {
		panic("InternalReleaseService: db is required")
	}
	if spiceDB == nil {
		panic("InternalReleaseService: spiceDB is required")
	}
	if ogRefresher == nil {
		panic("InternalReleaseService: OG refresher is required")
	}
	if artists == nil {
		panic("InternalReleaseService: artist summaries are required")
	}
	service := &InternalReleaseService{
		db: db, spiceDB: spiceDB, ogRefresher: ogRefresher, artists: artists,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalReleaseService) LoadReleaseBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadReleaseBlockDocumentRequest],
) (*connect.Response[intrav1.LoadReleaseBlockDocumentResponse], error) {
	if s.contentBlocks == nil || req.Msg == nil {
		return nil, errs.InternalMsg("Release content Block store is not configured")
	}
	locale, err := normalizeReleaseDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	var sourceRow releaseLocaleMetadataRow
	var targetRow *releaseLocaleMetadataRow
	var sourceNotes map[string]string
	var targetNotes map[string]string
	var artists []*intrav1.ReleaseArtistState
	bootstrap, err := loadReleaseContentBlockBootstrap(
		ctx, s.db, s.contentBlocks, s.spiceDB, req.Msg.ReleaseId,
		req.Msg.Principal,
		func(ctx context.Context, tx *gorm.DB) error {
			source, err := loadReleaseSourceLocale(ctx, tx, req.Msg.ReleaseId)
			if err != nil {
				return err
			}
			var sourceExists bool
			sourceRow, sourceExists, err = loadOptionalReleaseLocaleMetadataRow(ctx, tx, req.Msg.ReleaseId, source.SourceLocale, false)
			if err != nil {
				return err
			}
			if !sourceExists {
				return errs.FailedPrecondition("Release source locale metadata is missing")
			}
			sourceNotes, err = loadReleaseCreditLocaleNotes(ctx, tx, req.Msg.ReleaseId, source.SourceLocale)
			if err != nil {
				return err
			}
			if locale == source.SourceLocale {
				copy := sourceRow
				targetRow, targetNotes = &copy, maps.Clone(sourceNotes)
			} else {
				row, exists, loadErr := loadOptionalReleaseLocaleMetadataRow(ctx, tx, req.Msg.ReleaseId, locale, false)
				if loadErr != nil {
					return loadErr
				}
				if exists {
					targetRow = &row
					targetNotes, err = loadReleaseCreditLocaleNotes(ctx, tx, req.Msg.ReleaseId, locale)
					if err != nil {
						return err
					}
				}
			}
			artists, err = s.loadReleaseArtistStates(ctx, tx, req.Msg.ReleaseId)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if targetRow == nil && releaseSnapshotContainsLocale(bootstrap.Snapshot, locale) {
		return nil, errs.FailedPrecondition("Release target locale Blocks exist without owning metadata")
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
		SourceMetadata: releaseLocaleMetadataMessage(sourceRow, sourceNotes), Artists: artists,
		Locale: locale, LocaleExists: targetRow != nil,
		PresentLocaleValues: presentLocaleValues,
	}
	if targetRow != nil {
		response.LocaleMetadata = releaseLocaleMetadataMessage(*targetRow, targetNotes)
		if locale != bootstrap.Source.SourceLocale {
			targetRevision, revisionErr := deriveReleaseTargetRevision(response.DocumentRevision, *targetRow)
			if revisionErr != nil {
				return nil, revisionErr
			}
			response.TargetRevision = &targetRevision
		}
	}
	return connect.NewResponse(response), nil
}

func (s *InternalReleaseService) ApplyReleaseBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyReleaseBlockBatchRequest],
) (*connect.Response[intrav1.ApplyReleaseBlockBatchResponse], error) {
	if s.contentBlocks == nil || req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	locale, err := normalizeReleaseDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadReleaseContentDocumentID(ctx, s.db, req.Msg.ReleaseId)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(req.Msg.Batch, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
	if err != nil {
		return nil, errs.InvalidArgument("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		req.Msg.Batch.Profile,
		locale,
		&storage,
		req.Msg.AffectedLocaleValues,
	); err != nil {
		return nil, errs.InvalidArgument("affected_locale_values", err.Error())
	}
	expected, err := uuid.Parse(req.Msg.Batch.ExpectedRevision)
	if err != nil || expected == uuid.Nil {
		return nil, errs.InvalidArgument("batch.expected_revision", "must be a UUID")
	}
	var result contentblock.Result
	var target releaseTargetLocaleMutationResult
	var source releaseSourceLocale
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, err = loadReleaseSourceLocaleWithStrength(ctx, tx, req.Msg.ReleaseId, "UPDATE")
		if err != nil {
			return err
		}
		if err := requireDocumentContributors(ctx, tx, req.Msg.Batch.ContributorMemberIds); err != nil {
			return err
		}
		if err := requireReleaseCollaborationContributors(ctx, tx, s.checkpoints, req.Msg.ReleaseId, req.Msg.Batch.ContributorMemberIds); err != nil {
			return err
		}
		if locale != source.SourceLocale {
			if len(storage.BaseUpserts) != 0 || len(storage.Deletes) != 0 || len(storage.Moves) != 0 {
				return errs.CollaborationMutationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN, "Release target locale cannot mutate the shared Block graph")
			}
			for _, group := range storage.LocaleGroups {
				if group.Locale != locale {
					return errs.CollaborationMutationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH, "Release target locale mutation must match the authenticated room locale")
				}
			}
			batch, batchErr := contentblock.BatchFromRichTextStorage(documentID, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, storage)
			if batchErr != nil {
				return batchErr
			}
			target, err = applyReleaseTargetLocaleMutation(ctx, tx, s.contentBlocks, releaseTargetLocaleMutationInput{
				ReleaseID: req.Msg.ReleaseId, DocumentID: documentID, Locale: locale,
				ExpectedDocumentRevision: expected, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
				Batch: batch, AllowCreate: false, Now: time.Now().UTC(), Fence: internalReleaseContentFence(req.Msg.ReleaseId),
			})
			if err != nil || !target.Content.Changed {
				return err
			}
			return appendReleaseRequestTargetLocaleAudit(
				ctx, tx, s.auditWriter, req.Msg.ReleaseId, locale, sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Release room cannot carry a target revision")
		}
		for _, group := range storage.LocaleGroups {
			if group.Locale != source.SourceLocale {
				return errs.CollaborationMutationRejection(intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH, "Release source mutation must match the current source locale")
			}
		}
		batch, batchErr := contentblock.BatchFromRichTextProto(documentID, req.Msg.Batch)
		if batchErr != nil {
			return batchErr
		}
		result, err = s.contentBlocks.ApplyBatch(ctx, tx, batch, internalReleaseContentFence(req.Msg.ReleaseId))
		if err != nil {
			return err
		}
		if result.TranslationSourceChanged {
			err = s.ogRefresher.RequestCurrent(ctx, tx, req.Msg.ReleaseId, "release_source_document_saved")
		}
		return err
	})
	if err != nil {
		return nil, normalizeReleaseTargetWriterError(err)
	}
	response := &intrav1.ApplyReleaseBlockBatchResponse{Locale: locale}
	if locale == source.SourceLocale {
		response.DocumentRevision, response.Changed = result.DocumentRevision.String(), result.Changed
		response.SourceChanged, response.ChangedLocales = result.TranslationSourceChanged, result.ChangedLocales
	} else {
		response.DocumentRevision, response.Changed = target.Content.DocumentRevision.String(), target.Content.Changed
		response.ChangedLocales = target.Content.ChangedLocales
		revision := target.TargetRevision
		response.TargetRevision = &revision
	}
	return connect.NewResponse(response), nil
}

func (s *InternalReleaseService) UpdateReleaseLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateReleaseLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateReleaseLocaleMetadataResponse], error) {
	input := req.Msg
	if s.contentBlocks == nil || input == nil {
		return nil, errs.InternalMsg("Release content Block store is not configured")
	}
	if input == nil || (input.Title == nil && input.CreditNotes == nil) {
		return nil, errs.InvalidArgument("update", "at least one metadata value is required")
	}
	locale, err := normalizeReleaseDocumentLocale(input.Locale)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := uuid.Parse(input.ExpectedRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return nil, errs.InvalidArgument("expected_revision", "must be a UUID")
	}
	documentID, err := loadReleaseContentDocumentID(ctx, s.db, input.ReleaseId)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var source releaseSourceLocale
	var sourceResult contentblock.AdvanceResult
	var targetResult releaseTargetLocaleMutationResult
	var sourceTitleChanged bool
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireDocumentContributors(ctx, tx, input.ContributorMemberIds); err != nil {
			return err
		}
		if err := requireReleaseCollaborationContributors(ctx, tx, s.checkpoints, input.ReleaseId, input.ContributorMemberIds); err != nil {
			return err
		}
		source, err = loadReleaseSourceLocaleWithStrength(ctx, tx, input.ReleaseId, "UPDATE")
		if err != nil {
			return err
		}
		currentCreditIDs, err := loadReleaseCreditIDs(ctx, tx, input.ReleaseId)
		if err != nil {
			return err
		}
		var requestedNotes *map[string]string
		if input.CreditNotes != nil {
			values, validationErr := validateReleaseCreditNotes(
				input.CreditNotes.Values,
				currentCreditIDs,
				locale != source.SourceLocale,
			)
			if validationErr != nil {
				return validationErr
			}
			requestedNotes = &values
		}
		if locale != source.SourceLocale {
			targetResult, err = applyReleaseTargetLocaleMutation(ctx, tx, s.contentBlocks, releaseTargetLocaleMutationInput{
				ReleaseID: input.ReleaseId, DocumentID: documentID, Locale: locale,
				ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: input.ExpectedTargetRevision,
				AllowCreate: false,
				Batch:       contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: releaseContributorUUIDs(input.ContributorMemberIds)},
				SetTitle:    input.Title != nil, Title: input.Title, ReplaceCreditNotes: requestedNotes,
				Now: now, Fence: internalReleaseContentFence(input.ReleaseId),
			})
			if err != nil || !targetResult.Content.Changed {
				return err
			}
			return appendReleaseRequestTargetLocaleAudit(
				ctx, tx, s.auditWriter, input.ReleaseId, locale, sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		if input.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Release room cannot carry a target revision")
		}
		sourceResult, err = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			internalReleaseContentFence(input.ReleaseId),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				current, err := loadReleaseLocaleMetadataState(
					ctx, tx, input.ReleaseId, source.SourceLocale,
				)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				currentNotes, err := loadReleaseCreditLocaleNotes(
					ctx, tx, input.ReleaseId, source.SourceLocale,
				)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				var currentTitle *string
				if current != nil {
					currentTitle = current.Title
				}
				nextTitle := currentTitle
				if input.Title != nil {
					normalizedTitle := strings.TrimSpace(*input.Title)
					if normalizedTitle == "" {
						return contentblock.MetadataEffect{}, errs.InvalidArgument("title", "must not be empty")
					}
					nextTitle = &normalizedTitle
				}
				resolvedNotes := currentNotes
				if requestedNotes != nil {
					resolvedNotes = maps.Clone(*requestedNotes)
				}
				titleChanged := !releaseOptionalStringsEqual(currentTitle, nextTitle)
				notesChanged := !maps.Equal(currentNotes, resolvedNotes)
				if !titleChanged && !notesChanged {
					return contentblock.MetadataEffect{}, nil
				}
				save := translationLocaleDocumentSaveInput{
					Title:               nextTitle,
					OverwriteNullFields: true,
					Now:                 now,
				}
				err = saveReleaseSourceLocaleDocumentState(ctx, tx, input.ReleaseId, source.SourceLocale, save)
				sourceTitleChanged = titleChanged
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if notesChanged {
					if err := replaceReleaseCreditLocaleNotes(
						ctx, tx, input.ReleaseId, source.SourceLocale, resolvedNotes, now,
					); err != nil {
						return contentblock.MetadataEffect{}, err
					}
				}
				if err := touchReleaseSourceLocaleState(ctx, tx, input.ReleaseId, now); err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return contentblock.MetadataEffect{
					Changed:                  true,
					AffectsTranslationSource: true,
				}, nil
			},
		)
		if err != nil {
			return normalizeReleaseContentBlockError(err)
		}
		if sourceResult.TranslationSourceChanged {
			if sourceTitleChanged {
				return s.ogRefresher.RequestCurrent(ctx, tx, input.ReleaseId, "release_source_title_saved")
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, normalizeReleaseTargetWriterError(err)
	}
	response := &intrav1.UpdateReleaseLocaleMetadataResponse{Locale: locale}
	if locale == source.SourceLocale {
		response.DocumentRevision, response.Changed = sourceResult.DocumentRevision.String(), sourceResult.Changed
		response.SourceChanged, response.ChangedLocales = sourceResult.TranslationSourceChanged, []string{source.SourceLocale}
	} else {
		response.DocumentRevision, response.Changed = targetResult.Content.DocumentRevision.String(), targetResult.Content.Changed
		response.ChangedLocales = targetResult.Content.ChangedLocales
		revision := targetResult.TargetRevision
		response.TargetRevision = &revision
	}
	return connect.NewResponse(response), nil
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
	values []*intrav1.ReleaseCreditNote,
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
			return nil, errs.InvalidArgument("credit_notes", "must not contain null entries")
		}
		creditID := strings.TrimSpace(value.CreditId)
		note := strings.TrimSpace(value.Note)
		if creditID == "" {
			return nil, errs.InvalidArgument("credit_notes", "credit_id is required")
		}
		if note == "" && !allowEmpty {
			return nil, errs.InvalidArgument("credit_notes", "note must not be empty")
		}
		if _, ok := allowed[creditID]; !ok {
			return nil, errs.InvalidArgument("credit_notes", "credit_id does not belong to the Release")
		}
		if _, duplicate := notes[creditID]; duplicate {
			return nil, errs.InvalidArgument("credit_notes", "credit_id must be unique")
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

func (s *InternalReleaseService) loadReleaseArtistStates(
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
		return nil, errs.Internal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArtistID)
	}
	summaries, err := s.artists.LoadArtistSummariesWithDB(ctx, db, ids)
	if err != nil {
		return nil, errs.Internal(err)
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

package work

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// AIDocumentState is the Work-owned coherent metadata and typed Block state
// consumed by the DCDP adapter. The adapter, rather than this domain package,
// owns conversion to compact protocol nodes and operations.
type AIDocumentState struct {
	Work             model.Work
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	DocumentRevision string
	TargetRevision   *string
	Title            *string
	Summary          *string
	Snapshot         contentblock.Snapshot
	Document         *contentv1.LocalizedRichTextDocument
	ViewerMemberID   string
}

// AIDocumentMetadataPatch contains only Work-owned locale values. SetTitle and
// SetSummary distinguish omission from an explicit empty or NULL value.
type AIDocumentMetadataPatch struct {
	EnsureLocale bool
	SetTitle     bool
	Title        *string
	SetSummary   bool
	Summary      *string
}

// AIDocumentMutation is a generated Block batch plus the Work-owned metadata
// effect that must commit under the shared document and exact target CAS.
type AIDocumentMutation struct {
	WorkID                 string
	Locale                 string
	ObservedSourceLocale   string
	ExpectedRevision       uuid.UUID
	ExpectedTargetRevision *string
	ObservedLocaleExists   bool
	Batch                  contentblock.Batch
	Metadata               AIDocumentMetadataPatch
	CreateTranslation      bool
	DeleteTranslation      bool
	ContributorMemberID    string
}

type AIDocumentMutationResult struct {
	Content        contentblock.Result
	TargetRevision *string
}

// AIDocumentService owns Work authorization, lifecycle, aggregate locking and
// atomic metadata+Block persistence for DCDP clients.
type AIDocumentService struct{ internal *InternalWorkService }

var errRollbackWorkAIDocumentValidation = errors.New("rollback Work AI document validation")

func NewAIDocumentService(internal *InternalWorkService) (*AIDocumentService, error) {
	if internal == nil || internal.db == nil || internal.spiceDB == nil || internal.contentBlocks == nil {
		return nil, errors.New("work AI document service dependencies are required")
	}
	return &AIDocumentService{internal: internal}, nil
}

func (s *AIDocumentService) Load(ctx context.Context, workID, locale string) (AIDocumentState, error) {
	if err := validateWorkAIDocumentIdentity(workID, locale); err != nil {
		return AIDocumentState{}, err
	}
	var state AIDocumentState
	err := s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadWorkAIDocumentRoot(ctx, tx, workID, "SHARE")
		if err != nil {
			return err
		}
		principal, err := authorizeWorkAIDocument(
			ctx, tx, s.internal.spiceDB, root, workAuthorizationRead,
		)
		if err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, documentID, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

func validateWorkAIDocumentIdentity(workID, locale string) error {
	if _, err := parseWorkContentUUID("work_id", workID); err != nil {
		return err
	}
	if _, err := normalizeWorkDocumentLocale(locale); err != nil {
		return err
	}
	return nil
}

func (s *AIDocumentService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root model.Work,
	documentID uuid.UUID,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	state, err := loadWorkTargetLocaleState(ctx, tx, s.internal.contentBlocks, root.ID, documentID, locale, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	metadata := workLocaleMetadataRow{}
	exists := locale == state.SourceLocale
	var targetRevision *string
	if exists {
		metadata = state.SourceMetadata
	} else if state.TargetMetadata != nil {
		metadata = *state.TargetMetadata
		exists = true
		revision := state.TargetRevision
		targetRevision = &revision
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeWorkContentBlockError(err)
	}
	return AIDocumentState{
		Work: root, SourceLocale: state.SourceLocale,
		Locale: locale, LocaleExists: exists, DocumentRevision: state.Snapshot.Document.Revision.String(),
		TargetRevision: targetRevision, Title: metadata.Title, Summary: metadata.Summary,
		Snapshot: state.Snapshot, Document: document, ViewerMemberID: viewerMemberID,
	}, nil
}

func (s *AIDocumentService) Apply(ctx context.Context, mutation AIDocumentMutation) (AIDocumentMutationResult, error) {
	return s.ExecuteAIDocumentMutation(
		ctx, mutation.WorkID, mutation.Locale, AIDocumentExecutionApply,
		func(AIDocumentState) (AIDocumentMutation, error) { return mutation, nil },
	)
}

// Validate executes the exact generated/File/domain mutation path and rolls it
// back, keeping DCDP Validate and Apply behavior identical.
func (s *AIDocumentService) Validate(ctx context.Context, mutation AIDocumentMutation) error {
	_, err := s.ExecuteAIDocumentMutation(
		ctx, mutation.WorkID, mutation.Locale, AIDocumentExecutionValidate,
		func(AIDocumentState) (AIDocumentMutation, error) { return mutation, nil },
	)
	return err
}

func (s *AIDocumentService) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
) (AIDocumentMutationResult, error) {
	if _, err := parseWorkContentUUID("work_id", mutation.WorkID); err != nil {
		return AIDocumentMutationResult{}, err
	}
	if mutation.ExpectedRevision == uuid.Nil || mutation.Batch.DocumentID == uuid.Nil {
		return AIDocumentMutationResult{}, errs.InvalidArgument("expected_revision", "is required")
	}
	contributorIDs := workAIContributorIDs(mutation.ContributorMemberID)
	if len(contributorIDs) != 1 || len(mutation.Batch.ContributorMemberIDs) != 1 ||
		mutation.Batch.ContributorMemberIDs[0] != contributorIDs[0] {
		return AIDocumentMutationResult{}, errs.InvalidArgument("contributor", "compiled attribution must match the current Member")
	}
	if mutation.Batch.ExpectedRevision != mutation.ExpectedRevision {
		return AIDocumentMutationResult{}, errs.InvalidArgument("expected_revision", "does not match the compiled Block batch")
	}
	if strings.TrimSpace(mutation.ObservedSourceLocale) == "" {
		return AIDocumentMutationResult{}, errs.InvalidArgument("source_locale", "observed source locale is required")
	}
	if mutation.Locale != mutation.ObservedSourceLocale {
		if len(mutation.Batch.Upserts) != 0 || len(mutation.Batch.Deletes) != 0 || len(mutation.Batch.Reorders) != 0 {
			return AIDocumentMutationResult{}, errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Work target locale cannot mutate the shared Block graph",
			)
		}
		for _, group := range mutation.Batch.LocaleGroups {
			if group.Locale != mutation.Locale {
				return AIDocumentMutationResult{}, errs.CollaborationMutationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
					"Work target mutation must match the authorized locale",
				)
			}
		}
	}
	if mutation.Locale == mutation.ObservedSourceLocale &&
		(mutation.Metadata.EnsureLocale || mutation.CreateTranslation || mutation.DeleteTranslation) {
		return AIDocumentMutationResult{}, errs.InvalidArgument("locale", "Work source translation lifecycle cannot be changed")
	}
	if mutation.CreateTranslation && (mutation.DeleteTranslation || mutation.Metadata.SetTitle || mutation.Metadata.SetSummary || hasWorkAIDocumentBatchChanges(mutation.Batch)) {
		return AIDocumentMutationResult{}, errs.InvalidArgument("operations", "translation creation must be the only mutation")
	}
	if mutation.DeleteTranslation && (mutation.CreateTranslation || mutation.Metadata != AIDocumentMetadataPatch{} || hasWorkAIDocumentBatchChanges(mutation.Batch)) {
		return AIDocumentMutationResult{}, errs.InvalidArgument("operations", "translation deletion must be the only mutation")
	}

	now := time.Now().UTC()
	if mutation.Locale != mutation.ObservedSourceLocale {
		patch := workTargetMetadataPatch{
			EnsureLocale: mutation.Metadata.EnsureLocale || mutation.CreateTranslation,
			UpdateTitle:  mutation.Metadata.SetTitle, Title: mutation.Metadata.Title,
			UpdateSummary: mutation.Metadata.SetSummary, Summary: mutation.Metadata.Summary,
			DeleteLocale: mutation.DeleteTranslation,
		}
		result, targetRevision, err := applyWorkTargetLocaleBatch(
			ctx, tx, s.internal.contentBlocks, mutation.WorkID, mutation.Batch.DocumentID,
			mutation.Locale, mutation.Batch, mutation.ExpectedTargetRevision, patch,
			true, !mutation.ObservedLocaleExists, false, false, now, fence,
		)
		if err != nil {
			return AIDocumentMutationResult{}, err
		}
		if result.Changed {
			operation := sharedtelemetry.AuditItemOperationUpdated
			if !mutation.ObservedLocaleExists {
				operation = sharedtelemetry.AuditItemOperationCreated
			}
			if mutation.DeleteTranslation {
				operation = sharedtelemetry.AuditItemOperationDeleted
			}
			if err := appendWorkMemberTargetLocaleAudit(
				ctx, tx, s.internal.auditWriter, mutation.ContributorMemberID,
				mutation.WorkID, mutation.Locale, operation,
			); err != nil {
				return AIDocumentMutationResult{}, err
			}
		}
		return AIDocumentMutationResult{Content: result, TargetRevision: targetRevision}, nil
	}
	if mutation.ExpectedTargetRevision != nil {
		return AIDocumentMutationResult{}, errs.InvalidArgument("expected_target_revision", "source Work mutation cannot carry a target revision")
	}

	var metadataEffect contentblock.MetadataEffect
	var err error
	result, err := s.internal.contentBlocks.ApplyBatchWithMetadata(
		ctx, tx, mutation.Batch, fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			metadataEffect, err = applyWorkAIDocumentMetadata(
				ctx, tx, mutation.WorkID, mutation.Locale,
				mutation.ObservedLocaleExists, mutation.Metadata, now,
			)
			return metadataEffect, err
		},
	)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	if !result.Changed {
		return AIDocumentMutationResult{Content: result}, nil
	}
	if err := tx.WithContext(ctx).Model(&model.Work{}).
		Where("id = ?", mutation.WorkID).UpdateColumn("updated_at", now).Error; err != nil {
		return AIDocumentMutationResult{}, err
	}
	return AIDocumentMutationResult{Content: result}, nil
}

func loadWorkAIDocumentRoot(ctx context.Context, tx *gorm.DB, workID, lock string) (model.Work, uuid.UUID, error) {
	var root model.Work
	query := tx.WithContext(ctx).Table("work").Where("id = ?", workID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Work{}, uuid.Nil, errs.NotFound("work", workID)
		}
		return model.Work{}, uuid.Nil, errs.Internal(err)
	}
	if root.ContentDocumentID == nil {
		return model.Work{}, uuid.Nil, errs.FailedPrecondition("Work content document is not initialized")
	}
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	if err != nil || documentID == uuid.Nil {
		return model.Work{}, uuid.Nil, errs.InternalMsg("Work content document ID is invalid")
	}
	return root, documentID, nil
}

type workAIDocumentLocale struct {
	Title   *string
	Summary *string
}

func applyWorkAIDocumentMetadata(
	ctx context.Context,
	tx *gorm.DB,
	workID, locale string,
	observedExists bool,
	patch AIDocumentMetadataPatch,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	var source struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("source_locale").
		Where("id = ?", workID).
		Take(&source).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	if locale != source.SourceLocale || patch.EnsureLocale {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("locale", "Work source metadata mutation must target the current source locale")
	}
	current, exists, err := loadWorkAIDocumentLocaleForUpdate(ctx, tx, workID, locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != observedExists {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Work translation presence changed; reload before saving")
	}
	if !exists {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Work source translation is missing")
	}
	nextTitle := cloneOptionalString(current.Title)
	nextSummary := cloneOptionalString(current.Summary)
	if patch.SetTitle {
		nextTitle = cloneOptionalString(patch.Title)
	}
	if patch.SetSummary {
		nextSummary = cloneOptionalString(patch.Summary)
	}
	if nextTitle == nil || strings.TrimSpace(*nextTitle) == "" {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("title", "Work source title cannot be empty")
	}
	changed := !exists || !nullableStringEqual(current.Title, nextTitle) || !nullableStringEqual(current.Summary, nextSummary)
	if !changed {
		return contentblock.MetadataEffect{}, nil
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO work_translation (entity_id, locale, title, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title, summary = EXCLUDED.summary, updated_at = EXCLUDED.updated_at
	`, workID, locale, nextTitle, nextSummary, now, now).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: true,
		SourceLocale: source.SourceLocale, ChangedLocales: []string{locale},
	}, nil
}

func loadWorkAIDocumentLocaleForUpdate(ctx context.Context, tx *gorm.DB, workID, locale string) (workAIDocumentLocale, bool, error) {
	var row struct {
		Title   sql.NullString `gorm:"column:title"`
		Summary sql.NullString `gorm:"column:summary"`
	}
	result := tx.WithContext(ctx).
		Table("work_translation").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("title", "summary").
		Where("entity_id = ? AND locale = ?", workID, locale).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return workAIDocumentLocale{}, false, nil
	}
	if result.Error != nil {
		return workAIDocumentLocale{}, false, errs.Internal(result.Error)
	}
	return workAIDocumentLocale{Title: nullableStringFromSQL(row.Title), Summary: nullableStringFromSQL(row.Summary)}, true, nil
}

func authorizeWorkAIDocument(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	root model.Work,
	use workAuthorizationUse,
) (*auth.UserInfo, error) {
	principal, err := lockWorkAIDocumentPrincipal(ctx, tx)
	if err != nil {
		return nil, err
	}
	normal := workAction(policyv1.Work.View)
	if use == workAuthorizationMutation {
		normal = policyv1.Work.Edit
	}
	action := workLifecycleAction(string(root.Status), normal, use)
	if err := requireWorkPermissionForCurrentActor(ctx, checker, root.ID, action); err != nil {
		return nil, err
	}
	return principal, nil
}

func lockWorkAIDocumentPrincipal(ctx context.Context, tx *gorm.DB) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return nil, errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !active {
		return nil, errs.NoPermission("edit", "work")
	}
	return principal, nil
}

func workAIContributorIDs(memberID string) []uuid.UUID {
	parsed, err := uuid.Parse(memberID)
	if err != nil || parsed == uuid.Nil {
		return nil
	}
	return []uuid.UUID{parsed}
}

func hasWorkAIDocumentBatchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}

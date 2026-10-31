package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
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

var errRollbackWorkAIDocumentValidation = errors.New("rollback Work AI document validation")

// LoadAIDocument reads the authorized Work state of one locale for a DCDP
// client.
func (s *Service) LoadAIDocument(ctx context.Context, workID, locale string) (AIDocumentState, error) {
	if err := validateWorkAIDocumentIdentity(workID, locale); err != nil {
		return AIDocumentState{}, err
	}
	var root model.Work
	var documentID uuid.UUID
	var principal *auth.UserInfo
	access := mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		var err error
		if root, documentID, err = loadWorkAIDocumentRoot(ctx, tx, workID, clause.LockingStrengthShare); err != nil {
			return err
		}
		principal, err = authorizeWorkAIDocument(ctx, tx, s.spiceDB, root, workAuthorizationRead)
		return err
	})
	var state AIDocumentState
	err := s.readLocked(ctx, access, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		state, err = s.loadAIDocumentStateAfterAuthorization(ctx, tx, root, documentID, locale, principal.MemberID.String())
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

func (s *Service) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root model.Work,
	documentID uuid.UUID,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	state, err := loadWorkTargetLocaleState(ctx, tx, s.contentBlocks, root.ID, documentID, locale, false)
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

// workAIDocumentApply is the outcome of an applied AI document mutation.
type workAIDocumentApply struct {
	result    AIDocumentMutationResult
	operation sharedtelemetry.AuditItemOperation
	audited   bool
}

func (s *Service) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
) (workAIDocumentApply, error) {
	if err := validateWorkAIDocumentMutation(mutation); err != nil {
		return workAIDocumentApply{}, err
	}
	now := time.Now().UTC()
	if mutation.Locale != mutation.ObservedSourceLocale {
		return s.applyWorkAIDocumentTarget(ctx, tx, mutation, fence, now)
	}
	if mutation.ExpectedTargetRevision != nil {
		return workAIDocumentApply{}, domainerr.InvalidField("expected_target_revision", "source Work mutation cannot carry a target revision")
	}
	result, err := s.contentBlocks.ApplyBatchWithMetadata(
		ctx, tx, mutation.Batch, fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyWorkAIDocumentMetadata(
				ctx, tx, mutation.WorkID, mutation.Locale,
				mutation.ObservedLocaleExists, mutation.Metadata, now,
			)
		},
	)
	applied := workAIDocumentApply{
		result:    AIDocumentMutationResult{Content: result},
		operation: sharedtelemetry.AuditItemOperationUpdated,
		audited:   result.Changed,
	}
	if err != nil || !result.Changed {
		return applied, err
	}
	return applied, touchWork(ctx, tx, mutation.WorkID, now)
}

// validateWorkAIDocumentMutation rejects a compiled mutation whose shape
// the Work document cannot take.
func validateWorkAIDocumentMutation(mutation AIDocumentMutation) error {
	if _, err := parseWorkContentUUID("work_id", mutation.WorkID); err != nil {
		return err
	}
	if mutation.ExpectedRevision == uuid.Nil || mutation.Batch.DocumentID == uuid.Nil {
		return domainerr.InvalidField("expected_revision", "is required")
	}
	contributorID, parseErr := uuid.Parse(mutation.ContributorMemberID)
	if parseErr != nil || contributorID == uuid.Nil || len(mutation.Batch.ContributorMemberIDs) != 1 ||
		mutation.Batch.ContributorMemberIDs[0] != contributorID {
		return domainerr.InvalidField("contributor", "compiled attribution must match the current Member").WithCause(parseErr)
	}
	if mutation.Batch.ExpectedRevision != mutation.ExpectedRevision {
		return domainerr.InvalidField("expected_revision", "does not match the compiled Block batch")
	}
	if strings.TrimSpace(mutation.ObservedSourceLocale) == "" {
		return domainerr.InvalidField("source_locale", "observed source locale is required")
	}
	if mutation.Locale != mutation.ObservedSourceLocale {
		if err := validateWorkAIDocumentTargetBatch(mutation); err != nil {
			return err
		}
	} else if mutation.Metadata.EnsureLocale || mutation.CreateTranslation || mutation.DeleteTranslation {
		return domainerr.InvalidField("locale", "Work source translation lifecycle cannot be changed")
	}
	if mutation.CreateTranslation && (mutation.DeleteTranslation || mutation.Metadata.SetTitle || mutation.Metadata.SetSummary || hasWorkAIDocumentBatchChanges(mutation.Batch)) {
		return domainerr.InvalidField("operations", "translation creation must be the only mutation")
	}
	if mutation.DeleteTranslation && (mutation.CreateTranslation || mutation.Metadata != AIDocumentMetadataPatch{} || hasWorkAIDocumentBatchChanges(mutation.Batch)) {
		return domainerr.InvalidField("operations", "translation deletion must be the only mutation")
	}
	return nil
}

// validateWorkAIDocumentTargetBatch rejects a target locale batch that
// changes the shared Block graph or another locale.
func validateWorkAIDocumentTargetBatch(mutation AIDocumentMutation) error {
	if len(mutation.Batch.Upserts) != 0 || len(mutation.Batch.Deletes) != 0 || len(mutation.Batch.Reorders) != 0 {
		return domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Work target locale cannot mutate the shared Block graph",
		)
	}
	for _, group := range mutation.Batch.LocaleGroups {
		if group.Locale != mutation.Locale {
			return domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Work target mutation must match the authorized locale",
			)
		}
	}
	return nil
}

func (s *Service) applyWorkAIDocumentTarget(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
	now time.Time,
) (workAIDocumentApply, error) {
	patch := workTargetMetadataPatch{
		EnsureLocale: mutation.Metadata.EnsureLocale || mutation.CreateTranslation,
		UpdateTitle:  mutation.Metadata.SetTitle, Title: mutation.Metadata.Title,
		UpdateSummary: mutation.Metadata.SetSummary, Summary: mutation.Metadata.Summary,
		DeleteLocale: mutation.DeleteTranslation,
	}
	result, targetRevision, err := applyWorkTargetLocaleMutation(ctx, tx, s.contentBlocks, workTargetLocaleMutationInput{
		WorkID: mutation.WorkID, DocumentID: mutation.Batch.DocumentID, Locale: mutation.Locale,
		Batch: mutation.Batch, ExpectedTargetRevision: mutation.ExpectedTargetRevision, Patch: patch,
		Policy: targetlocale.Policy{AllowCreate: true, SeedSource: !mutation.ObservedLocaleExists},
		Now:    now, Fence: fence,
	})
	applied := workAIDocumentApply{
		result:    AIDocumentMutationResult{Content: result, TargetRevision: targetRevision},
		operation: sharedtelemetry.AuditItemOperationUpdated,
		audited:   result.Changed,
	}
	switch {
	case mutation.DeleteTranslation:
		applied.operation = sharedtelemetry.AuditItemOperationDeleted
	case !mutation.ObservedLocaleExists:
		applied.operation = sharedtelemetry.AuditItemOperationCreated
	}
	return applied, err
}

func loadWorkAIDocumentRoot(ctx context.Context, tx *gorm.DB, workID, lock string) (model.Work, uuid.UUID, error) {
	var root model.Work
	query := tx.WithContext(ctx).Table("work").Where("id = ?", workID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Work{}, uuid.Nil, domainerr.ResourceNotFound("work", workID)
		}
		return model.Work{}, uuid.Nil, err
	}
	if root.ContentDocumentID == nil {
		return model.Work{}, uuid.Nil, domainerr.Precondition("Work content document is not initialized")
	}
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	if err != nil || documentID == uuid.Nil {
		return model.Work{}, uuid.Nil, fmt.Errorf("Work content document ID %q is invalid", *root.ContentDocumentID)
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("source_locale").
		Where("id = ?", workID).
		Take(&source).Error; err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if locale != source.SourceLocale || patch.EnsureLocale {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("locale", "Work source metadata mutation must target the current source locale")
	}
	current, exists, err := loadWorkAIDocumentLocaleForUpdate(ctx, tx, workID, locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != observedExists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("Work translation presence changed; reload before saving")
	}
	if !exists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("Work source translation is missing")
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
		return contentblock.MetadataEffect{}, domainerr.InvalidField("title", "Work source title cannot be empty")
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
		return contentblock.MetadataEffect{}, err
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("title", "summary").
		Where("entity_id = ? AND locale = ?", workID, locale).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return workAIDocumentLocale{}, false, nil
	}
	if result.Error != nil {
		return workAIDocumentLocale{}, false, result.Error
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
	action := workLifecycleAction(root.Status, normal, use)
	if err := requireWorkPermissionForCurrentActor(ctx, checker, root.ID, action); err != nil {
		return nil, err
	}
	return principal, nil
}

func lockWorkAIDocumentPrincipal(ctx context.Context, tx *gorm.DB) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return nil, domainerr.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, domainerr.NoPermission("edit", "work")
	}
	return principal, nil
}

func hasWorkAIDocumentBatchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}

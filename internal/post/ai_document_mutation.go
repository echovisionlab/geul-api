package post

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// AIDocumentMetadataPatch is the Post-owned metadata portion of one DCDP
// mutation. The generated Rich Text batch remains owned by contentblock.
type AIDocumentMetadataPatch struct {
	EnsureLocale  bool
	SetTitle      bool
	Title         *string
	SetSummary    bool
	Summary       *string
	SetCategories bool
	CategoryIDs   []string
	SetTags       bool
	TagIDs        []string
}

type AIDocumentMutation struct {
	PostID                 string
	Locale                 string
	ObservedSourceLocale   string
	ObservedLocaleExists   bool
	ExpectedRevision       uuid.UUID
	ExpectedTargetRevision *string
	ContributorMemberID    uuid.UUID
	Batch                  contentblock.Batch
	Metadata               AIDocumentMetadataPatch
	DeleteTranslation      bool
}

type AIDocumentMutationResult struct {
	Result         contentblock.Result
	TargetRevision *string
}

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target"
)

type AIDocumentRevisionConflictError struct {
	Kind                    AIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return "Post AI document revision changed: current document revision is " + e.CurrentDocumentRevision
}

var errRollbackPostAIDocumentValidation = errors.New("rollback Post AI document validation")

// AIDocumentExecutionMode selects whether the exact owning-domain mutation
// transaction commits or deliberately rolls back after running the same
// compiler, authorization, CAS, validation, and persistence path.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler belongs to the schema adapter. The Post domain
// provides its locked current state and receives only its own typed mutation;
// DCDP types do not cross into this package.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type postAIDocumentCompilerError struct{ cause error }

func (e *postAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *postAIDocumentCompilerError) Unwrap() error { return e.cause }

type postAIDocumentMutationEffects struct {
	changedFields []string
	titleChanged  bool
	// operation is what the edit did to its locale's content: a target
	// translation it created or deleted, otherwise an update.
	operation sharedtelemetry.AuditItemOperation
}

func (e *postAIDocumentMutationEffects) addField(field string) {
	if e == nil || slices.Contains(e.changedFields, field) {
		return
	}
	e.changedFields = append(e.changedFields, field)
}

// ExecuteAIDocumentMutation is the Post reference boundary for DCDP Validate
// and Apply. It locks the Post root, selects Edit or EditArchived from the
// locked status, performs exactly one fully-consistent SpiceDB decision, and
// only then exposes the current authorized state to the adapter compiler. The
// compiled Post mutation is applied under the same transaction and root lock.
func (s *Service) ExecuteAIDocumentMutation(
	ctx context.Context,
	postID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Post AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	normalizedLocale, err := validatePostAIDocumentIdentity(postID, locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root aiDocumentPostRoot
	var compiled AIDocumentMutation
	var validated AIDocumentMutationResult
	access := mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		var err error
		if root, err = loadPostAIDocumentRoot(ctx, tx, postID, clause.LockingStrengthUpdate); err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(require, postID, root.Status, policyv1.Post.Edit); err != nil {
			if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
				return domainerr.ResourceNotFound("post", postID).WithCause(err)
			}
			return err
		}
		return nil
	})
	result, _, err := runPostMutation(ctx, s, mutation.Spec[AIDocumentMutationResult]{Access: access},
		func(ctx context.Context, tx *gorm.DB, effects *postEffects) (AIDocumentMutationResult, error) {
			principal := auth.GetUser(ctx)
			if principal == nil || principal.MemberID == "" {
				return AIDocumentMutationResult{}, domainerr.ResourceNotFound("post", postID)
			}
			state, err := s.loadAIDocumentStateAfterAuthorization(ctx, tx, root, normalizedLocale, principal.MemberID.String())
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
			if compiled, err = compiler(state); err != nil {
				return AIDocumentMutationResult{}, &postAIDocumentCompilerError{cause: err}
			}
			documentID, err := validateCompiledPostAIDocumentMutation(state, compiled)
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
			aiEffects := &postAIDocumentMutationEffects{operation: sharedtelemetry.AuditItemOperationUpdated}
			result, err := s.applyAIDocumentMutationInTransaction(
				ctx, tx, compiled, aiEffects, effects,
				postAuthorizedAIDocumentFence(root, documentID, contentblock.DomainContext{SourceLocale: state.SourceLocale}),
			)
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
			if mode == AIDocumentExecutionValidate {
				validated = result
				return result, errRollbackPostAIDocumentValidation
			}
			recordPostAIDocumentEdit(effects, compiled, result, aiEffects)
			return result, nil
		})
	if errors.Is(err, errRollbackPostAIDocumentValidation) {
		return validated, nil
	}
	if err != nil {
		return AIDocumentMutationResult{}, postAIDocumentFailure(err, compiled)
	}
	return result, nil
}

// recordPostAIDocumentEdit names the locale content record of an applied
// edit that changed the document, and its content.updated signal.
func recordPostAIDocumentEdit(
	effects *postEffects,
	compiled AIDocumentMutation,
	result AIDocumentMutationResult,
	aiEffects *postAIDocumentMutationEffects,
) {
	if result.Result.Changed {
		effects.audits = append(effects.audits, postLocaleContentAudit(
			compiled.ContributorMemberID.String(), compiled.PostID, compiled.Locale, aiEffects.operation,
		))
	}
	if event := postAIDocumentUpdatedEvent(compiled, result.Result, aiEffects); event != nil {
		effects.signal(event)
	}
}

// postAIDocumentFailure answers a failed AI document mutation: a compiler
// failure as itself, a revision conflict with the current revisions.
func postAIDocumentFailure(err error, compiled AIDocumentMutation) error {
	var compilerErr *postAIDocumentCompilerError
	var conflict *AIDocumentRevisionConflictError
	var stale *contentblock.StaleRevisionError
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &compilerErr):
		return compilerErr.cause
	case errors.As(err, &conflict):
		return conflict
	case errors.As(err, &stale):
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String(),
		}
	case errors.As(err, &targetConflict):
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: compiled.ExpectedRevision.String(),
			CurrentTargetRevision: postAIDocumentTargetConflictRevision(targetConflict),
		}
	default:
		return normalizePostContentBlockError(err)
	}
}

func validateCompiledPostAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
) (uuid.UUID, error) {
	documentID, err := uuid.Parse(state.ContentDocumentID)
	if err != nil || documentID == uuid.Nil || documentID.String() != state.ContentDocumentID {
		return uuid.Nil, domainerr.Precondition("Post content document identity is invalid").WithCause(failurecause.ServerFault(err))
	}
	if mutation.PostID != state.PostID || mutation.Locale != state.RequestedLocale ||
		mutation.ContributorMemberID.String() != state.ViewerMemberID ||
		mutation.ObservedSourceLocale != state.SourceLocale ||
		mutation.ObservedLocaleExists != state.LocaleExists ||
		mutation.Batch.DocumentID != documentID {
		return uuid.Nil, domainerr.InvalidField(
			"mutation",
			"compiled Post identity, locale, contributor, source observation, and content document must match the locked state",
		)
	}
	if mutation.ExpectedRevision.String() != state.DocumentRevision ||
		!optional.Equal(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, domainerr.InvalidField(
			"mutation",
			"compiled Post document and target revisions must match the locked state",
		)
	}
	return documentID, nil
}

func postAIDocumentUpdatedEvent(
	mutation AIDocumentMutation,
	result contentblock.Result,
	effects *postAIDocumentMutationEffects,
) *managev1.ContentUpdatedEvent {
	if !result.Changed || !result.TranslationSourceChanged {
		return nil
	}
	var fields []string
	if effects != nil {
		fields = append(fields, effects.changedFields...)
	}
	if result.ContentChanged && !slices.Contains(fields, "content") {
		fields = append(fields, "content")
	}
	return buildPostLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             mutation.PostID,
		Fields:               fields,
		DocumentRevision:     result.DocumentRevision.String(),
		Contributors:         []string{mutation.ContributorMemberID.String()},
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:               mutation.Locale,
		DocumentStateChanged: true,
	})
}

func (s *Service) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	compiled AIDocumentMutation,
	aiEffects *postAIDocumentMutationEffects,
	effects *postEffects,
	fence contentblock.DomainFence,
) (AIDocumentMutationResult, error) {
	if err := validatePostAIDocumentMutation(compiled); err != nil {
		return AIDocumentMutationResult{}, err
	}
	now := dbtime.Now()
	if compiled.Locale != compiled.ObservedSourceLocale {
		return s.applyPostAIDocumentTarget(ctx, tx, compiled, aiEffects, effects, fence, now)
	}
	if compiled.ExpectedTargetRevision != nil {
		return AIDocumentMutationResult{}, domainerr.InvalidField("expected_target_revision", "source Post mutation cannot carry a target revision")
	}
	result, err := s.contentBlocks.ApplyBatchWithMetadata(ctx, tx, compiled.Batch, fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyPostAIDocumentMetadata(ctx, tx, compiled, compiled.ObservedSourceLocale, now, aiEffects)
		},
	)
	if err != nil || !result.Changed {
		return AIDocumentMutationResult{Result: result}, err
	}
	if err := touchPost(ctx, tx, compiled.PostID, now); err != nil {
		return AIDocumentMutationResult{}, err
	}
	if aiEffects.titleChanged {
		effects.requestOG(compiled.PostID, compiled.Locale, false, "post_ai_document_saved")
	}
	return AIDocumentMutationResult{Result: result}, nil
}

// validatePostAIDocumentMutation rejects a compiled mutation whose shape the
// Post document cannot take.
func validatePostAIDocumentMutation(compiled AIDocumentMutation) error {
	if compiled.PostID == "" || strings.TrimSpace(compiled.Locale) == "" {
		return domainerr.InvalidField("document", "Post ID and locale are required")
	}
	if compiled.ExpectedRevision == uuid.Nil || compiled.ContributorMemberID == uuid.Nil ||
		compiled.Batch.DocumentID == uuid.Nil || compiled.Batch.ExpectedRevision != compiled.ExpectedRevision {
		return domainerr.InvalidField("revision", "compiled document, revision, and contributor are required")
	}
	if len(compiled.Batch.ContributorMemberIDs) != 1 || compiled.Batch.ContributorMemberIDs[0] != compiled.ContributorMemberID {
		return domainerr.InvalidField("contributor", "compiled attribution must match the current Member")
	}
	if compiled.ObservedSourceLocale == "" {
		return domainerr.InvalidField("source_locale", "observed source locale is required")
	}
	if compiled.Locale != compiled.ObservedSourceLocale {
		if err := validatePostAIDocumentTargetBatch(compiled); err != nil {
			return err
		}
	} else if compiled.Metadata.EnsureLocale || compiled.DeleteTranslation {
		return domainerr.InvalidField("locale", "Post source translation lifecycle cannot be changed")
	}
	if compiled.DeleteTranslation && (hasPostAIDocumentBatchChanges(compiled.Batch) || hasPostAIDocumentMetadataPatch(compiled.Metadata)) {
		return domainerr.InvalidField("operations", "translation deletion must be exclusive")
	}
	return nil
}

func validatePostAIDocumentTargetBatch(compiled AIDocumentMutation) error {
	if len(compiled.Batch.Upserts) != 0 || len(compiled.Batch.Deletes) != 0 || len(compiled.Batch.Reorders) != 0 {
		return domainerr.InvalidField("operations", "non-source locale cannot mutate the Post block graph")
	}
	for _, group := range compiled.Batch.LocaleGroups {
		if group.Locale != compiled.Locale {
			return domainerr.InvalidField("operations", "compiled locale mutation targets another locale")
		}
	}
	return nil
}

func (s *Service) applyPostAIDocumentTarget(
	ctx context.Context,
	tx *gorm.DB,
	compiled AIDocumentMutation,
	aiEffects *postAIDocumentMutationEffects,
	effects *postEffects,
	fence contentblock.DomainFence,
	now time.Time,
) (AIDocumentMutationResult, error) {
	if !compiled.ObservedLocaleExists && !compiled.Metadata.EnsureLocale &&
		!compiled.DeleteTranslation && !hasPostAIDocumentBatchChanges(compiled.Batch) &&
		!hasPostAIDocumentMetadataPatch(compiled.Metadata) {
		return AIDocumentMutationResult{Result: contentblock.Result{DocumentRevision: compiled.ExpectedRevision}}, nil
	}
	if compiled.DeleteTranslation {
		result, err := deletePostTargetLocale(ctx, tx, s.contentBlocks, postTargetLocaleDeleteInput{
			PostID: compiled.PostID, Locale: compiled.Locale,
			ExpectedDocumentRevision: compiled.ExpectedRevision,
			ExpectedTargetRevision:   compiled.ExpectedTargetRevision,
			ContributorMemberIDs:     []uuid.UUID{compiled.ContributorMemberID},
			Now:                      now,
			Fence:                    fence,
		})
		aiEffects.operation = sharedtelemetry.AuditItemOperationDeleted
		return AIDocumentMutationResult{Result: result}, err
	}
	target, err := applyPostTargetLocaleMutation(ctx, tx, s.contentBlocks, postTargetLocaleMutationInput{
		PostID: compiled.PostID, Locale: compiled.Locale,
		ExpectedDocumentRevision: compiled.ExpectedRevision,
		ExpectedTargetRevision:   compiled.ExpectedTargetRevision,
		Batch:                    &compiled.Batch,
		Policy: targetlocale.Policy{
			AllowCreate: compiled.Metadata.EnsureLocale, SeedSource: compiled.Metadata.EnsureLocale,
		},
		SetTitle:   compiled.Metadata.SetTitle,
		Title:      compiled.Metadata.Title,
		SetSummary: compiled.Metadata.SetSummary,
		Summary:    compiled.Metadata.Summary,
		Now:        now,
		Fence:      fence,
	})
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	if target.LocaleCreated {
		aiEffects.operation = sharedtelemetry.AuditItemOperationCreated
	}
	if compiled.Metadata.SetTitle && target.MetadataChanged {
		aiEffects.addField("title")
	}
	if compiled.Metadata.SetSummary && target.MetadataChanged {
		aiEffects.addField("summary")
	}
	if target.TitleChanged {
		aiEffects.titleChanged = true
		effects.requestOG(compiled.PostID, compiled.Locale, false, "post_ai_document_saved")
	}
	documentRevision, err := uuid.Parse(target.DocumentRevision)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	targetRevision := target.TargetRevision
	return AIDocumentMutationResult{
		Result: contentblock.Result{
			DocumentRevision: documentRevision,
			Changed:          target.Changed,
			ContentChanged:   target.ContentChanged,
			MetadataChanged:  target.MetadataChanged,
			ChangedLocales:   []string{compiled.Locale},
		},
		TargetRevision: &targetRevision,
	}, nil
}

func postAuthorizedAIDocumentFence(
	root aiDocumentPostRoot,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.ContentDocumentID == nil || requestedDocumentID != documentID ||
			strings.TrimSpace(*root.ContentDocumentID) != documentID.String() {
			return contentblock.DomainContext{}, domainerr.Precondition("Post content document changed; reload before saving")
		}
		return domain, nil
	}
}

func postAIDocumentConflict(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) error {
	var document contentblock.Document
	if err := tx.WithContext(ctx).Table("content_document").
		Select("revision").
		Where("id = ?", documentID).
		Take(&document).Error; err != nil {
		return err
	}
	return &AIDocumentRevisionConflictError{
		Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: document.Revision.String(),
	}
}

func postAIDocumentTargetConflictRevision(conflict *translation.TargetRevisionConflict) *string {
	if conflict == nil || !conflict.CurrentExists {
		return nil
	}
	revision := conflict.CurrentRevision
	return &revision
}

func loadPostAIDocumentMetadataForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	postID, locale string,
) (AIDocumentLocaleMetadata, bool, error) {
	var row aiDocumentMetadataRow
	err := tx.WithContext(ctx).
		Table("post_translation").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("title", "summary").
		Where("entity_id = ?::uuid AND locale = ?", postID, locale).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AIDocumentLocaleMetadata{}, false, nil
	}
	if err != nil {
		return AIDocumentLocaleMetadata{}, false, err
	}
	return AIDocumentLocaleMetadata(row), true, nil
}

func applyPostAIDocumentMetadata(
	ctx context.Context,
	tx *gorm.DB,
	compiled AIDocumentMutation,
	sourceLocale string,
	now time.Time,
	effects *postAIDocumentMutationEffects,
) (contentblock.MetadataEffect, error) {
	if strings.TrimSpace(sourceLocale) == "" {
		return contentblock.MetadataEffect{}, domainerr.Precondition("Post source locale is not initialized")
	}
	current, exists, err := loadPostAIDocumentMetadataForUpdate(ctx, tx, compiled.PostID, compiled.Locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != compiled.ObservedLocaleExists {
		return contentblock.MetadataEffect{}, postAIDocumentConflict(ctx, tx, compiled.Batch.DocumentID)
	}
	if compiled.Locale == sourceLocale && !exists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("Post source locale metadata is missing")
	}
	patch := compiled.Metadata
	if (patch.SetCategories || patch.SetTags) && compiled.Locale != sourceLocale {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("locale", "only the source locale may change Post taxonomy")
	}
	if !exists && !patch.EnsureLocale && (patch.SetTitle || patch.SetSummary || len(compiled.Batch.LocaleGroups) != 0) {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("locale", "absent Post translation must be created with the same mutation")
	}
	localeChanged := false
	if !exists && patch.EnsureLocale {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO post_translation (
				entity_id, locale, created_at, updated_at
			) VALUES (?::uuid, ?, ?, ?)`,
			compiled.PostID, compiled.Locale, now, now,
		).Error; err != nil {
			return contentblock.MetadataEffect{}, err
		}
		exists, localeChanged = true, true
	}
	textChanged := false
	if exists {
		if textChanged, err = applyPostAIDocumentText(ctx, tx, compiled, current, sourceLocale, now, effects); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	}
	taxonomyChanged, err := applyPostAIDocumentTaxonomy(ctx, tx, compiled, effects)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	effect := contentblock.MetadataEffect{
		Changed:                  localeChanged || textChanged || taxonomyChanged,
		AffectsTranslationSource: textChanged && compiled.Locale == sourceLocale,
	}
	if localeChanged || textChanged {
		effect.ChangedLocales = []string{compiled.Locale}
	}
	return effect, nil
}

// applyPostAIDocumentText writes the requested title and summary of the
// mutation's locale and reports whether they changed.
func applyPostAIDocumentText(
	ctx context.Context,
	tx *gorm.DB,
	compiled AIDocumentMutation,
	current AIDocumentLocaleMetadata,
	sourceLocale string,
	now time.Time,
	effects *postAIDocumentMutationEffects,
) (bool, error) {
	patch := compiled.Metadata
	updates := map[string]any{}
	if patch.SetTitle {
		if compiled.Locale == sourceLocale && (patch.Title == nil || strings.TrimSpace(*patch.Title) == "") {
			return false, domainerr.InvalidField("title", "source title must not be empty")
		}
		if !postAIDocumentNullableEqual(current.Title, patch.Title) {
			updates["title"] = patch.Title
			effects.addField("title")
			effects.titleChanged = true
		}
	}
	if patch.SetSummary && !postAIDocumentNullableEqual(current.Summary, patch.Summary) {
		updates["summary"] = patch.Summary
		effects.addField("summary")
	}
	if len(updates) == 0 {
		return false, nil
	}
	updates["updated_at"] = now
	return true, tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ?::uuid AND locale = ?", compiled.PostID, compiled.Locale).
		Updates(updates).Error
}

// applyPostAIDocumentTaxonomy replaces the requested Categories and Tags and
// reports whether either changed.
func applyPostAIDocumentTaxonomy(ctx context.Context, tx *gorm.DB, compiled AIDocumentMutation, effects *postAIDocumentMutationEffects) (bool, error) {
	currentCategories, currentTags, err := loadPostTaxonomyIDs(ctx, tx, compiled.PostID)
	if err != nil {
		return false, err
	}
	changed := false
	for _, taxonomy := range []struct {
		set      bool
		ids      []string
		current  []string
		table    string
		junction string
		column   string
		field    string
	}{
		{compiled.Metadata.SetCategories, compiled.Metadata.CategoryIDs, currentCategories, "category", "post_category", "category_id", "categoryIds"},
		{compiled.Metadata.SetTags, compiled.Metadata.TagIDs, currentTags, "tag", "post_tag", "tag_id", "tagIds"},
	} {
		if !taxonomy.set {
			continue
		}
		next := sliceutil.UniqueTrimmed(taxonomy.ids)
		slices.Sort(next)
		if slices.Equal(taxonomy.current, next) {
			continue
		}
		if err := replacePostTaxonomyLinks(ctx, tx, taxonomy.table, taxonomy.junction, taxonomy.column, compiled.PostID, next); err != nil {
			return false, err
		}
		changed = true
		effects.addField(taxonomy.field)
	}
	return changed, nil
}

func hasPostAIDocumentBatchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}

func hasPostAIDocumentMetadataPatch(patch AIDocumentMetadataPatch) bool {
	return patch.EnsureLocale || patch.SetTitle || patch.SetSummary || patch.SetCategories || patch.SetTags
}

func postAIDocumentNullableEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

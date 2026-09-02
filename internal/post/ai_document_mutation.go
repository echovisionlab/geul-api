package post

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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
func (s *PostService) ExecuteAIDocumentMutation(
	ctx context.Context,
	postID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Post AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Post AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	normalizedLocale, err := validatePostAIDocumentIdentity(postID, locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}

	var result AIDocumentMutationResult
	effects := &postAIDocumentMutationEffects{}
	var mutation AIDocumentMutation
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadPostAIDocumentRoot(ctx, tx, postID, "UPDATE")
		if err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(
			ctx,
			tx,
			s.spiceDB,
			postID,
			root.Status,
			policyv1.Post.Edit,
		); err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return errs.NotFound("post", postID)
			default:
				return err
			}
		}
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return errs.NotFound("post", postID)
		}
		state, err := s.loadAIDocumentStateAfterAuthorization(
			ctx,
			tx,
			root,
			normalizedLocale,
			principal.MemberID.String(),
		)
		if err != nil {
			return err
		}
		mutation, err = compiler(state)
		if err != nil {
			return &postAIDocumentCompilerError{cause: err}
		}
		documentID, err := validateCompiledPostAIDocumentMutation(state, mutation)
		if err != nil {
			return err
		}
		domain := contentblock.DomainContext{SourceLocale: state.SourceLocale}
		result, err = s.applyAIDocumentMutationInTransaction(
			ctx,
			tx,
			mutation,
			effects,
			postAuthorizedAIDocumentFence(root, documentID, domain),
		)
		if err != nil {
			return err
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackPostAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackPostAIDocumentValidation) {
		return result, nil
	}
	if err != nil {
		var compilerErr *postAIDocumentCompilerError
		var conflict *AIDocumentRevisionConflictError
		var stale *contentblock.StaleRevisionError
		var targetConflict *translation.TargetRevisionConflict
		switch {
		case errors.As(err, &compilerErr):
			return AIDocumentMutationResult{}, compilerErr.cause
		case errors.As(err, &conflict):
			return AIDocumentMutationResult{}, conflict
		case errors.As(err, &stale):
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: stale.CurrentRevision.String(),
			}
		case errors.As(err, &targetConflict):
			return AIDocumentMutationResult{}, &AIDocumentRevisionConflictError{
				Kind: AIDocumentTargetRevisionConflict, CurrentDocumentRevision: mutation.ExpectedRevision.String(),
				CurrentTargetRevision: postAIDocumentTargetConflictRevision(targetConflict),
			}
		default:
			return AIDocumentMutationResult{}, normalizePostContentBlockError(err)
		}
	}
	if mode == AIDocumentExecutionApply {
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, postAIDocumentUpdatedEvent(mutation, result.Result, effects))
	}
	return result, nil
}

func validateCompiledPostAIDocumentMutation(
	state AIDocumentState,
	mutation AIDocumentMutation,
) (uuid.UUID, error) {
	documentID, err := uuid.Parse(state.ContentDocumentID)
	if err != nil || documentID == uuid.Nil || documentID.String() != state.ContentDocumentID {
		return uuid.Nil, errs.FailedPrecondition("Post content document identity is invalid")
	}
	if mutation.PostID != state.PostID || mutation.Locale != state.RequestedLocale ||
		mutation.ContributorMemberID.String() != state.ViewerMemberID ||
		mutation.ObservedSourceLocale != state.SourceLocale ||
		mutation.ObservedLocaleExists != state.LocaleExists ||
		mutation.Batch.DocumentID != documentID {
		return uuid.Nil, errs.InvalidArgument(
			"mutation",
			"compiled Post identity, locale, contributor, source observation, and content document must match the locked state",
		)
	}
	if mutation.ExpectedRevision.String() != state.DocumentRevision ||
		!equalPostAIDocumentTargetRevision(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return uuid.Nil, errs.InvalidArgument(
			"mutation",
			"compiled Post document and target revisions must match the locked state",
		)
	}
	return documentID, nil
}

func equalPostAIDocumentTargetRevision(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	return buildPostBlockContentUpdatedEvent(
		mutation.PostID,
		fields,
		result.DocumentRevision.String(),
		[]string{mutation.ContributorMemberID.String()},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		mutation.Locale,
		true,
		nil,
		true,
	)
}

func (s *PostService) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	effects *postAIDocumentMutationEffects,
	fence contentblock.DomainFence,
) (AIDocumentMutationResult, error) {
	if s.spiceDB == nil || s.contentBlocks == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Post AI document")
	}
	if mutation.PostID == "" || strings.TrimSpace(mutation.Locale) == "" {
		return AIDocumentMutationResult{}, errs.InvalidArgument("document", "Post ID and locale are required")
	}
	if mutation.ExpectedRevision == uuid.Nil || mutation.ContributorMemberID == uuid.Nil ||
		mutation.Batch.DocumentID == uuid.Nil || mutation.Batch.ExpectedRevision != mutation.ExpectedRevision {
		return AIDocumentMutationResult{}, errs.InvalidArgument("revision", "compiled document, revision, and contributor are required")
	}
	if len(mutation.Batch.ContributorMemberIDs) != 1 || mutation.Batch.ContributorMemberIDs[0] != mutation.ContributorMemberID {
		return AIDocumentMutationResult{}, errs.InvalidArgument("contributor", "compiled attribution must match the current Member")
	}
	if mutation.ObservedSourceLocale == "" {
		return AIDocumentMutationResult{}, errs.InvalidArgument("source_locale", "observed source locale is required")
	}
	if mutation.Locale != mutation.ObservedSourceLocale {
		if len(mutation.Batch.Upserts) != 0 || len(mutation.Batch.Deletes) != 0 || len(mutation.Batch.Reorders) != 0 {
			return AIDocumentMutationResult{}, errs.InvalidArgument("operations", "non-source locale cannot mutate the Post block graph")
		}
		for _, group := range mutation.Batch.LocaleGroups {
			if group.Locale != mutation.Locale {
				return AIDocumentMutationResult{}, errs.InvalidArgument("operations", "compiled locale mutation targets another locale")
			}
		}
	}
	if mutation.Locale == mutation.ObservedSourceLocale && (mutation.Metadata.EnsureLocale || mutation.DeleteTranslation) {
		return AIDocumentMutationResult{}, errs.InvalidArgument("locale", "Post source translation lifecycle cannot be changed")
	}
	if mutation.DeleteTranslation && (hasPostAIDocumentBatchChanges(mutation.Batch) || hasPostAIDocumentMetadataPatch(mutation.Metadata)) {
		return AIDocumentMutationResult{}, errs.InvalidArgument("operations", "translation deletion must be exclusive")
	}

	now := time.Now().UTC()
	if mutation.Locale != mutation.ObservedSourceLocale {
		if !mutation.ObservedLocaleExists && !mutation.Metadata.EnsureLocale &&
			!mutation.DeleteTranslation && !hasPostAIDocumentBatchChanges(mutation.Batch) &&
			!hasPostAIDocumentMetadataPatch(mutation.Metadata) {
			return AIDocumentMutationResult{Result: contentblock.Result{
				DocumentRevision: mutation.ExpectedRevision,
			}}, nil
		}
		if mutation.DeleteTranslation {
			result, err := deletePostTargetLocale(ctx, tx, s.contentBlocks, postTargetLocaleDeleteInput{
				PostID: mutation.PostID, Locale: mutation.Locale,
				ExpectedDocumentRevision: mutation.ExpectedRevision,
				ExpectedTargetRevision:   mutation.ExpectedTargetRevision,
				ContributorMemberIDs:     []uuid.UUID{mutation.ContributorMemberID},
				Now:                      now,
				Fence:                    fence,
			})
			if err != nil {
				return AIDocumentMutationResult{}, err
			}
			return AIDocumentMutationResult{Result: result}, nil
		}
		target, err := applyPostTargetLocaleMutation(ctx, tx, s.contentBlocks, postTargetLocaleMutationInput{
			PostID: mutation.PostID, Locale: mutation.Locale,
			ExpectedDocumentRevision: mutation.ExpectedRevision,
			ExpectedTargetRevision:   mutation.ExpectedTargetRevision,
			Batch:                    &mutation.Batch,
			AllowCreate:              mutation.Metadata.EnsureLocale,
			SeedSourceOnCreate:       mutation.Metadata.EnsureLocale,
			SetTitle:                 mutation.Metadata.SetTitle,
			Title:                    mutation.Metadata.Title,
			SetSummary:               mutation.Metadata.SetSummary,
			Summary:                  mutation.Metadata.Summary,
			Now:                      now,
			Fence:                    fence,
		})
		if err != nil {
			return AIDocumentMutationResult{}, err
		}
		if mutation.Metadata.SetTitle && target.MetadataChanged {
			effects.addField("title")
		}
		if mutation.Metadata.SetSummary && target.MetadataChanged {
			effects.addField("summary")
		}
		if target.TitleChanged {
			effects.titleChanged = true
			if _, err := s.ogRefresher.RequestCurrentWithDB(
				ctx, tx,
				managev1.OgEntityType_OG_ENTITY_TYPE_POST,
				mutation.PostID, mutation.Locale, false, "post_ai_document_saved",
			); err != nil {
				return AIDocumentMutationResult{}, err
			}
		}
		documentRevision, parseErr := uuid.Parse(target.DocumentRevision)
		if parseErr != nil {
			return AIDocumentMutationResult{}, errs.Internal(parseErr)
		}
		targetRevision := target.TargetRevision
		return AIDocumentMutationResult{
			Result: contentblock.Result{
				DocumentRevision: documentRevision,
				Changed:          target.Changed,
				ContentChanged:   target.ContentChanged,
				MetadataChanged:  target.MetadataChanged,
				ChangedLocales:   []string{mutation.Locale},
			},
			TargetRevision: &targetRevision,
		}, nil
	}

	if mutation.ExpectedTargetRevision != nil {
		return AIDocumentMutationResult{}, errs.InvalidArgument("expected_target_revision", "source Post mutation cannot carry a target revision")
	}
	result, err := s.contentBlocks.ApplyBatchWithMetadata(
		ctx,
		tx,
		mutation.Batch,
		fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyPostAIDocumentMetadata(ctx, tx, mutation, mutation.ObservedSourceLocale, now, effects)
		},
	)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	if !result.Changed {
		return AIDocumentMutationResult{Result: result}, nil
	}
	if err := tx.WithContext(ctx).Model(&model.Post{}).
		Where("id = ?", mutation.PostID).
		UpdateColumn("updated_at", now).Error; err != nil {
		return AIDocumentMutationResult{}, err
	}
	if effects != nil && effects.titleChanged {
		if _, err := s.ogRefresher.RequestCurrentWithDB(
			ctx, tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_POST,
			mutation.PostID, mutation.Locale, false, "post_ai_document_saved",
		); err != nil {
			return AIDocumentMutationResult{}, err
		}
	}
	return AIDocumentMutationResult{Result: result}, nil
}

func postAuthorizedAIDocumentFence(
	root aiDocumentPostRoot,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.ContentDocumentID == nil || requestedDocumentID != documentID ||
			strings.TrimSpace(*root.ContentDocumentID) != documentID.String() {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Post content document changed; reload before saving")
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
		Clauses(clause.Locking{Strength: "UPDATE"}).
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
	mutation AIDocumentMutation,
	sourceLocale string,
	now time.Time,
	effects *postAIDocumentMutationEffects,
) (contentblock.MetadataEffect, error) {
	if strings.TrimSpace(sourceLocale) == "" {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Post source locale is not initialized")
	}
	current, exists, err := loadPostAIDocumentMetadataForUpdate(ctx, tx, mutation.PostID, mutation.Locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != mutation.ObservedLocaleExists {
		return contentblock.MetadataEffect{}, postAIDocumentConflict(ctx, tx, mutation.Batch.DocumentID)
	}
	if mutation.Locale == sourceLocale && !exists {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Post source locale metadata is missing")
	}
	patch := mutation.Metadata
	if (patch.SetCategories || patch.SetTags) && mutation.Locale != sourceLocale {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("locale", "only the source locale may change Post taxonomy")
	}
	changed, sourceChanged, localeChanged := false, false, false
	if !exists && !patch.EnsureLocale && (patch.SetTitle || patch.SetSummary || len(mutation.Batch.LocaleGroups) != 0) {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("locale", "absent Post translation must be created with the same mutation")
	}
	if !exists && patch.EnsureLocale {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO post_translation (
				entity_id, locale, created_at, updated_at
			) VALUES (?::uuid, ?, ?, ?)`,
			mutation.PostID,
			mutation.Locale,
			now,
			now,
		).Error; err != nil {
			return contentblock.MetadataEffect{}, err
		}
		exists, changed, localeChanged = true, true, true
	}
	if exists && (patch.SetTitle || patch.SetSummary) {
		updates := map[string]any{}
		if patch.SetTitle {
			if mutation.Locale == sourceLocale && (patch.Title == nil || strings.TrimSpace(*patch.Title) == "") {
				return contentblock.MetadataEffect{}, errs.InvalidArgument("title", "source title must not be empty")
			}
			if !postAIDocumentNullableEqual(current.Title, patch.Title) {
				updates["title"] = patch.Title
				effects.addField("title")
				if effects != nil {
					effects.titleChanged = true
				}
			}
		}
		if patch.SetSummary && !postAIDocumentNullableEqual(current.Summary, patch.Summary) {
			updates["summary"] = patch.Summary
			effects.addField("summary")
		}
		if len(updates) != 0 {
			updates["updated_at"] = now
			if err := tx.WithContext(ctx).Table("post_translation").
				Where("entity_id = ?::uuid AND locale = ?", mutation.PostID, mutation.Locale).
				Updates(updates).Error; err != nil {
				return contentblock.MetadataEffect{}, err
			}
			changed, localeChanged = true, true
			if mutation.Locale == sourceLocale {
				sourceChanged = true
			}
		}
	}
	currentCategories, currentTags, err := loadPostTaxonomyIDs(ctx, tx, mutation.PostID)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if patch.SetCategories {
		next := normalizeStringIDs(patch.CategoryIDs)
		slices.Sort(next)
		if !slices.Equal(currentCategories, next) {
			if err := replacePostTaxonomyLinks(ctx, tx, "category", "post_category", "category_id", mutation.PostID, next); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			changed = true
			effects.addField("categoryIds")
		}
	}
	if patch.SetTags {
		next := normalizeStringIDs(patch.TagIDs)
		slices.Sort(next)
		if !slices.Equal(currentTags, next) {
			if err := replacePostTaxonomyLinks(ctx, tx, "tag", "post_tag", "tag_id", mutation.PostID, next); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			changed = true
			effects.addField("tagIds")
		}
	}
	effect := contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: sourceChanged}
	if localeChanged {
		effect.ChangedLocales = []string{mutation.Locale}
	}
	return effect, nil
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

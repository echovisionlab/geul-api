package legal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// AIDocument is the Legal-owned, transport-neutral policy document projection.
// Rows retain the generated Rich Text storage contract so adapters never read
// Legal tables or reconstruct an editor-native document.
type AIDocument struct {
	EntityType      string
	EntityID        string
	DocumentID      uuid.UUID
	Revision        string
	SourceLocale    string
	Locale          string
	LocaleExists    bool
	TargetRevision  *string
	LocaleUpdatedAt *time.Time
	Title           *string
	Rows            []contentv1.ContentStorageRow
	ViewerMemberID  string
}

type AITranslationMutation uint8

const (
	AITranslationUnchanged AITranslationMutation = iota
	AITranslationCreate
	AITranslationDelete
)

// AIDocumentMutation is compiled by the protocol adapter from a validated
// DCDP operation batch. Legal remains the sole authority for authorization,
// lifecycle, aggregate CAS, translation resource CRUD, and derived projection.
type AIDocumentMutation struct {
	EntityType                     string
	EntityID                       string
	Locale                         string
	ExpectedRevision               string
	ExpectedTargetRevision         *string
	Content                        *contentblock.Batch
	SetTitle                       bool
	Title                          *string
	Translation                    AITranslationMutation
	ContributorMemberID            string
	AuthoritativeTargetReplacement bool
}

type AIDocumentMutationResult struct {
	Revision       string
	TargetRevision *string
	Changed        bool
}

type legalAIDocumentApplyResult struct {
	Content                 contentblock.Result
	TargetRevision          *string
	CurrentDocumentRevision string
}

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target"
)

type AIDocumentRevisionConflict struct {
	Kind                  AIDocumentRevisionConflictKind
	CurrentRevision       string
	CurrentTargetRevision *string
}

func (e *AIDocumentRevisionConflict) Error() string {
	return "Legal AI document revision changed; reload before saving"
}

// AIDocumentExecutionMode selects whether the exact Legal mutation commits or
// deliberately rolls back after the same authorization, compiler, CAS,
// projection and audit path has completed.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler receives only the current Legal state loaded
// after the history root lock and exact lifecycle-aware authorization.
type AIDocumentMutationCompiler func(AIDocument) (AIDocumentMutation, error)

type legalAIDocumentCompilerError struct{ cause error }

func (e *legalAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *legalAIDocumentCompilerError) Unwrap() error { return e.cause }

var errRollbackLegalAIDocumentValidation = errors.New("rollback Legal AI document validation")

type AIDocumentService struct {
	db            *gorm.DB
	contentBlocks *contentblock.Store
	spiceDB       CollaborationPermissionChecker
	legalOG       OG
	auditWriter   domainaudit.Appender
}

func NewAuditedAIDocumentService(
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	spiceDB CollaborationPermissionChecker,
	legalOG OG,
	auditWriter domainaudit.Appender,
) (*AIDocumentService, error) {
	if db == nil || contentBlocks == nil || spiceDB == nil || legalOG == nil || auditWriter == nil {
		return nil, errors.New("legal AI document dependencies are required")
	}
	return &AIDocumentService{
		db: db, contentBlocks: contentBlocks, spiceDB: spiceDB, legalOG: legalOG,
		auditWriter: auditWriter,
	}, nil
}

func (s *AIDocumentService) LoadAIDocument(
	ctx context.Context,
	entityType string,
	entityID string,
	locale string,
) (AIDocument, error) {
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocument{}, err
	}
	var result AIDocument
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, false)
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "view", true); err != nil {
			return err
		}
		policy, err := legalDocumentPolicyForType(entityType)
		if err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, entityType, entityID, legalViewAction(policy, root.Status),
		); err != nil {
			return err
		}
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return errs.AuthenticationRequired()
		}
		result, _, err = s.loadAIDocumentAfterAuthorization(
			ctx,
			tx,
			root,
			entityType,
			entityID,
			locale,
			principal.MemberID.String(),
		)
		return err
	})
	if err != nil {
		return AIDocument{}, normalizeLegalContentBlockError(entityType, err)
	}
	return result, nil
}

func (s *AIDocumentService) loadAIDocumentAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	entityType string,
	entityID string,
	locale string,
	viewerMemberID string,
) (AIDocument, string, error) {
	targetState, err := loadLegalTargetLocaleState(
		ctx, tx, s.contentBlocks, entityType, entityID, locale, false,
	)
	if err != nil {
		return AIDocument{}, "", err
	}
	sourceLocale := targetState.SourceLocale
	snapshot := targetState.Snapshot
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return AIDocument{}, "", err
	}
	rows, err := contentv1.FlattenRichTextDocumentStorage(
		document,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	)
	if err != nil {
		return AIDocument{}, "", err
	}
	var title *string
	exists := false
	if locale == sourceLocale {
		title = targetState.SourceMetadata.Title
		exists = true
	}
	var localeUpdatedAt *time.Time
	var targetRevision *string
	if targetState.TargetMetadata != nil {
		title = targetState.TargetMetadata.Title
		exists = true
		updatedAt := targetState.TargetMetadata.UpdatedAt
		localeUpdatedAt = &updatedAt
		revision := targetState.TargetRevision
		targetRevision = &revision
	}
	if locale != sourceLocale && !exists && legalAIRowsContainLocale(rows, locale) {
		return AIDocument{}, "", errs.FailedPrecondition(
			entityType + " target locale values exist without a translation resource",
		)
	}
	return AIDocument{
		EntityType: entityType, EntityID: entityID, DocumentID: snapshot.Document.ID,
		Revision:     snapshot.Document.Revision.String(),
		SourceLocale: sourceLocale, Locale: locale, LocaleExists: exists,
		TargetRevision:  targetRevision,
		LocaleUpdatedAt: localeUpdatedAt,
		Title:           title, Rows: rows, ViewerMemberID: viewerMemberID,
	}, sourceLocale, nil
}

func legalAIRowsContainLocale(rows []contentv1.ContentStorageRow, locale string) bool {
	for _, row := range rows {
		for _, localized := range row.Locales {
			if localized.Locale == locale {
				return true
			}
		}
	}
	return false
}

// ExecuteAIDocumentMutation is the Legal hard-cut DCDP mutation seam. The
// locked policy history selects Edit or EditArchived for exactly one SpiceDB
// decision. Missing or denied policy roots remain NotFound and the compiler is
// not invoked until that decision succeeds. The compiled mutation, revision
// CAS, audit, projection refresh and OG request then share the same transaction.
func (s *AIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	entityType string,
	entityID string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if s == nil || s.db == nil || s.contentBlocks == nil || s.spiceDB == nil || s.legalOG == nil || s.auditWriter == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Legal AI document")
	}
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable("Legal AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocumentMutationResult{}, err
	}

	var result legalAIDocumentApplyResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = s.executeAIDocumentMutationWithDB(
			ctx, tx, entityType, entityID, locale, compiler, true, false,
		)
		if err != nil {
			return err
		}
		if mode == AIDocumentExecutionValidate {
			return errRollbackLegalAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackLegalAIDocumentValidation) {
		output := AIDocumentMutationResult{
			Revision: result.Content.DocumentRevision.String(), TargetRevision: result.TargetRevision,
			Changed: result.Content.Changed,
		}
		return output, nil
	}
	return legalAIDocumentResult(entityType, result, err)
}

func legalAIDocumentMutationAccessError(entityType, entityID string, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return errs.NotFound(entityType, entityID)
	default:
		return err
	}
}

func validateCompiledLegalAIDocumentMutation(
	state AIDocument,
	mutation AIDocumentMutation,
	allowAuthoritativeTargetReplacement bool,
) error {
	expected, err := uuid.Parse(mutation.ExpectedRevision)
	if err != nil || expected == uuid.Nil || expected.String() != mutation.ExpectedRevision {
		return errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	contributor, err := uuid.Parse(mutation.ContributorMemberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != mutation.ContributorMemberID {
		return errs.AuthenticationRequired()
	}
	if mutation.EntityType != state.EntityType || mutation.EntityID != state.EntityID ||
		mutation.Locale != state.Locale || mutation.ExpectedRevision != state.Revision ||
		mutation.ContributorMemberID != state.ViewerMemberID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Legal identity, locale, contributor and revision must match the locked state",
		)
	}
	if mutation.AuthoritativeTargetReplacement && (!allowAuthoritativeTargetReplacement || state.Locale == state.SourceLocale) {
		return errs.InvalidArgument(
			"operations", "authoritative Legal target replacement is available only to the XLIFF interchange",
		)
	}
	if state.Locale == state.SourceLocale {
		if mutation.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
		}
	} else if err := translation.ValidateExpectedTargetRevision(
		mutation.ExpectedTargetRevision, targetRevisionValue(state.TargetRevision), state.LocaleExists,
	); err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			var currentTargetRevision *string
			if targetConflict.CurrentExists {
				current := targetConflict.CurrentRevision
				currentTargetRevision = &current
			}
			return &AIDocumentRevisionConflict{
				Kind: AIDocumentTargetRevisionConflict, CurrentRevision: state.Revision,
				CurrentTargetRevision: currentTargetRevision,
			}
		}
		return err
	}
	if mutation.Content != nil && (mutation.Content.DocumentID != state.DocumentID ||
		mutation.Content.ExpectedRevision != expected ||
		len(mutation.Content.ContributorMemberIDs) != 1 ||
		mutation.Content.ContributorMemberIDs[0] != contributor) {
		return errs.InvalidArgument("operations", "compiled Legal content mutation must match the locked state")
	}
	return nil
}

func targetRevisionValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *AIDocumentService) applyAIDocumentAfterAuthorizationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	lockedState AIDocument,
	sourceLocale string,
	mutation AIDocumentMutation,
	requestSavedOG bool,
) (legalAIDocumentApplyResult, error) {
	expected := uuid.MustParse(mutation.ExpectedRevision)
	contributor := uuid.MustParse(mutation.ContributorMemberID)
	if mutation.Locale == sourceLocale && mutation.Translation != AITranslationUnchanged {
		return legalAIDocumentApplyResult{}, errs.InvalidArgument("locale", "source locale is not a translation resource")
	}
	if mutation.Translation == AITranslationDelete && (mutation.Content != nil || mutation.SetTitle) {
		return legalAIDocumentApplyResult{}, errs.InvalidArgument(
			"operations", "deleting a legal translation cannot be combined with content or metadata writes",
		)
	}

	fence := legalAuthorizedAIDocumentFence(root, lockedState, sourceLocale)
	now := time.Now().UTC()
	output := legalAIDocumentApplyResult{
		CurrentDocumentRevision: lockedState.Revision,
	}
	if mutation.Locale != sourceLocale {
		localePreviouslyExists := lockedState.LocaleExists
		if mutation.Translation == AITranslationDelete {
			result, err := deleteLegalTargetLocale(
				ctx,
				tx,
				s.contentBlocks,
				legalTargetLocaleDeleteInput{
					EntityType: mutation.EntityType, EntityID: mutation.EntityID, Locale: mutation.Locale,
					ExpectedDocumentRevision: expected,
					ExpectedTargetRevision:   mutation.ExpectedTargetRevision,
					ContributorMemberIDs:     []uuid.UUID{contributor},
					Fence:                    fence,
				},
			)
			if err != nil {
				return legalAIDocumentApplyResult{}, err
			}
			output.Content = result
		} else {
			batch := contentblock.Batch{
				DocumentID: *root.ContentDocumentID, ExpectedRevision: expected,
				ContributorMemberIDs: []uuid.UUID{contributor},
			}
			if mutation.Content != nil {
				batch = *mutation.Content
			}
			target, err := applyLegalTargetLocaleMutation(
				ctx,
				tx,
				s.contentBlocks,
				legalTargetLocaleMutationInput{
					EntityType: mutation.EntityType, EntityID: mutation.EntityID, Locale: mutation.Locale,
					ExpectedDocumentRevision: expected,
					ExpectedTargetRevision:   mutation.ExpectedTargetRevision,
					Batch:                    batch,
					AllowCreate:              mutation.Translation == AITranslationCreate,
					SeedSourceOnCreate:       mutation.Translation == AITranslationCreate,
					AllowLocaleDeletes:       mutation.AuthoritativeTargetReplacement,
					SetTitle:                 mutation.SetTitle, Title: mutation.Title, Now: now, Fence: fence,
				},
			)
			if err != nil {
				return legalAIDocumentApplyResult{}, err
			}
			revision := target.TargetRevision
			output.TargetRevision = &revision
			output.Content = contentblock.Result{
				DocumentRevision: uuid.MustParse(target.DocumentRevision),
				Changed:          target.Changed,
				ChangedLocales:   []string{mutation.Locale},
			}
		}
		if output.Content.Changed {
			if err := appendLegalTargetLocaleContentAudit(
				ctx,
				tx,
				s.auditWriter,
				mutation.ContributorMemberID,
				mutation.EntityType,
				mutation.EntityID,
				root.Version,
				mutation.Locale,
				legalTargetLocaleContentOperation(mutation.Translation, localePreviouslyExists),
			); err != nil {
				return legalAIDocumentApplyResult{}, err
			}
		}
	} else {
		batch := contentblock.Batch{
			DocumentID: *root.ContentDocumentID, ExpectedRevision: expected,
			ContributorMemberIDs: []uuid.UUID{contributor},
		}
		if mutation.Content != nil {
			batch = *mutation.Content
		}
		result, err := s.contentBlocks.ApplyBatchWithMetadata(
			ctx, tx, batch, fence, s.aiMetadataMutation(mutation, sourceLocale, now),
		)
		if err != nil {
			return legalAIDocumentApplyResult{}, err
		}
		output.Content = result
	}
	if !output.Content.Changed {
		return output, nil
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(
		ctx,
		tx,
		*root.ContentDocumentID,
		sourceLocale,
	)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	documentService := internalLegalDocumentService{
		db: tx, kind: mutation.EntityType, contentBlocks: s.contentBlocks, legalOG: s.legalOG,
	}
	if err := documentService.refreshDerivedContentProjectionsWithDB(
		ctx,
		tx,
		mutation.EntityID,
		snapshot,
		sourceLocale,
		now,
	); err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if !requestSavedOG {
		return output, nil
	}
	if err := documentService.requestSavedOG(
		ctx,
		tx,
		mutation.EntityID,
		mutation.Locale,
		mutation.EntityType+"_ai_document_saved",
	); err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	return output, nil
}

func (s *AIDocumentService) aiMetadataMutation(
	mutation AIDocumentMutation,
	sourceLocale string,
	now time.Time,
) contentblock.MetadataMutation {
	return func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
		effect := contentblock.MetadataEffect{}
		if mutation.SetTitle {
			if mutation.Title == nil {
				return effect, errs.InvalidArgument("title", "source title cannot be absent")
			}
			normalized := strings.TrimSpace(*mutation.Title)
			if normalized == "" {
				return effect, errs.InvalidArgument("title", "must not be empty")
			}
			result := tx.WithContext(ctx).Table(mutation.EntityType+"_history").
				Where("id = ? AND title IS DISTINCT FROM ?", mutation.EntityID, normalized).
				Updates(structured.Fields{"title": normalized, "updated_at": now})
			if result.Error != nil {
				return effect, errs.Internal(result.Error)
			}
			effect.Changed = result.RowsAffected != 0
			effect.AffectsTranslationSource = effect.Changed
			effect.SourceLocale = sourceLocale
		}
		return effect, nil
	}
}

func legalAuthorizedAIDocumentFence(
	root legalContentDocumentRoot,
	lockedState AIDocument,
	sourceLocale string,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if root.ID == "" || root.ContentDocumentID == nil ||
			*root.ContentDocumentID != lockedState.DocumentID || documentID != lockedState.DocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition(
				lockedState.EntityType + " content document changed; reload before saving",
			)
		}
		return contentblock.DomainContext{
			SourceLocale: sourceLocale,
		}, nil
	}
}

func requireLegalAIPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return nil, errs.NoPermission("read", "legal document")
	}
	return principal, nil
}

func validateAIDocumentIdentity(entityType, entityID, locale string) error {
	if entityType != "privacy" && entityType != "terms" {
		return errs.InvalidArgument("entity_type", "must be privacy or terms")
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return errs.InvalidArgument("entity_id", "must be a UUID")
	}
	if _, err := canonicalLegalLocale(locale); err != nil {
		return err
	}
	return nil
}

func (m AIDocumentMutation) String() string {
	return fmt.Sprintf("%s/%s@%s", m.EntityType, m.EntityID, m.Locale)
}

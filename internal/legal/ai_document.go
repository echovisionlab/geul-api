package legal

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

// legalAIDocumentApplyResult is what one applied mutation changed.
type legalAIDocumentApplyResult struct {
	Content                 contentblock.Result
	TargetRevision          *string
	CurrentDocumentRevision string
	applied                 AIDocumentMutation
	localeChange            *legalLocaleContentChange
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

// AIDocumentDeps are the collaborators of the Legal AI document use cases.
// Every field is required; NewAIDocumentService rejects a missing one.
type AIDocumentDeps struct {
	DB            *gorm.DB
	ContentBlocks *contentblock.Store
	SpiceDB       *auth.SpiceDBClient
	OG            OG
	Audit         domainaudit.Appender
}

func (d AIDocumentDeps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "ContentBlocks": d.ContentBlocks == nil, "SpiceDB": d.SpiceDB == nil,
		"OG": d.OG == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("legal AI document dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// AIDocumentService holds the Legal AI document and translation
// interchange use cases.
type AIDocumentService struct {
	contentBlocks *contentblock.Store
	spiceDB       *auth.SpiceDBClient
	legalOG       OG
	auditWriter   domainaudit.Appender
	mutations     *mutation.Runner
}

// NewAIDocumentService validates deps and returns the Legal AI document use
// cases.
func NewAIDocumentService(deps AIDocumentDeps) (*AIDocumentService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &AIDocumentService{
		contentBlocks: deps.ContentBlocks, spiceDB: deps.SpiceDB, legalOG: deps.OG,
		auditWriter: deps.Audit, mutations: mutations,
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
	err := readLocked(ctx, s.mutations, mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, false)
		if err != nil {
			return err
		}
		kind, err := legalKind(entityType)
		if err != nil {
			return err
		}
		return authorizeLegal(ctx, tx, s.spiceDB, entityType, entityID, "view", true, legalViewAction(kind, root.Status))
	}), func(ctx context.Context, tx *gorm.DB) error {
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return domainerr.AuthenticationRequired()
		}
		var err error
		result, _, err = s.loadAIDocumentAfterAuthorization(ctx, tx, entityType, entityID, locale, principal.MemberID.String())
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
		return AIDocument{}, "", domainerr.Precondition(entityType + " target locale values exist without a translation resource")
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
	if compiler == nil {
		return AIDocumentMutationResult{}, domainerr.DependencyUnavailable("Legal AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	if err := validateAIDocumentIdentity(entityType, entityID, locale); err != nil {
		return AIDocumentMutationResult{}, err
	}

	var root legalContentDocumentRoot
	var memberID string
	var applied legalAIDocumentApplyResult
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[legalAIDocumentApplyResult]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			var err error
			root, memberID, err = s.authorizeAIDocumentEdit(ctx, tx, entityType, entityID)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (legalAIDocumentApplyResult, error) {
			var err error
			applied, err = s.compileAndApplyAIDocumentMutationWithDB(
				ctx, tx, root, entityType, entityID, locale, memberID, compiler, true, false,
			)
			if err == nil && mode == AIDocumentExecutionValidate {
				err = errRollbackLegalAIDocumentValidation
			}
			return applied, err
		},
		Audit: func(result legalAIDocumentApplyResult) mutation.Audit { return result.audit() },
		Outbox: func(ctx context.Context, tx *gorm.DB, result legalAIDocumentApplyResult) error {
			return result.publish(ctx, tx)
		},
	})
	if errors.Is(err, errRollbackLegalAIDocumentValidation) {
		return applied.output(), nil
	}
	return legalAIDocumentResult(entityType, applied, err)
}

// authorizeAIDocumentEdit locks the version and authorizes the request's
// principal to edit it in its lifecycle. A caller who may not learns nothing
// about the version: every rejection answers NotFound.
func (s *AIDocumentService) authorizeAIDocumentEdit(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) (legalContentDocumentRoot, string, error) {
	root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, true)
	if err != nil {
		return root, "", err
	}
	principal, err := requireLegalAIPrincipal(ctx)
	if err != nil {
		return root, "", legalAIDocumentMutationAccessError(entityType, entityID, err)
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return root, "", err
	}
	if !active {
		return root, "", domainerr.ResourceNotFound(entityType, entityID)
	}
	kind, err := legalKind(entityType)
	if err != nil {
		return root, "", err
	}
	if err := requireLegalPermission(ctx, s.spiceDB, entityType, entityID, legalMutationAction(kind, root.Status, legalActionEdit)); err != nil {
		return root, "", legalAIDocumentMutationAccessError(entityType, entityID, err)
	}
	return root, principal.MemberID.String(), nil
}

func legalAIDocumentMutationAccessError(entityType, entityID string, err error) error {
	if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
		return domainerr.ResourceNotFound(entityType, entityID).WithCause(err)
	}
	return err
}

func validateCompiledLegalAIDocumentMutation(
	state AIDocument,
	mutation AIDocumentMutation,
	allowAuthoritativeTargetReplacement bool,
) error {
	expected, err := uuid.Parse(mutation.ExpectedRevision)
	if err != nil || expected == uuid.Nil || expected.String() != mutation.ExpectedRevision {
		return domainerr.InvalidField("expected_revision", "must be a canonical UUID")
	}
	contributor, err := uuid.Parse(mutation.ContributorMemberID)
	if err != nil || contributor == uuid.Nil || contributor.String() != mutation.ContributorMemberID {
		return domainerr.AuthenticationRequired()
	}
	if mutation.EntityType != state.EntityType || mutation.EntityID != state.EntityID ||
		mutation.Locale != state.Locale || mutation.ExpectedRevision != state.Revision ||
		mutation.ContributorMemberID != state.ViewerMemberID {
		return domainerr.InvalidField("mutation", "compiled Legal identity, locale, contributor and revision must match the locked state")
	}
	if mutation.AuthoritativeTargetReplacement && (!allowAuthoritativeTargetReplacement || state.Locale == state.SourceLocale) {
		return domainerr.InvalidField("operations", "authoritative Legal target replacement is available only to the XLIFF interchange")
	}
	if state.Locale == state.SourceLocale {
		if mutation.ExpectedTargetRevision != nil {
			return domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
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
		return domainerr.InvalidField("operations", "compiled Legal content mutation must match the locked state")
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
	if mutation.Locale == sourceLocale && mutation.Translation != AITranslationUnchanged {
		return legalAIDocumentApplyResult{}, domainerr.InvalidField("locale", "source locale is not a translation resource")
	}
	if mutation.Translation == AITranslationDelete && (mutation.Content != nil || mutation.SetTitle) {
		return legalAIDocumentApplyResult{}, domainerr.InvalidField(
			"operations", "deleting a legal translation cannot be combined with content or metadata writes",
		)
	}
	write := legalAIDocumentWrite{
		root: root, locked: lockedState, sourceLocale: sourceLocale, mutation: mutation,
		fence: legalAuthorizedAIDocumentFence(root, lockedState, sourceLocale), now: dbtime.Now(),
	}
	var output legalAIDocumentApplyResult
	var err error
	if mutation.Locale != sourceLocale {
		output, err = s.applyAITargetLocale(ctx, tx, write)
	} else {
		output, err = s.applyAISourceLocale(ctx, tx, write)
	}
	if err != nil || !output.Content.Changed {
		return output, err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, sourceLocale)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if err := refreshDerivedContent(ctx, tx, mutation.EntityType, mutation.EntityID, snapshot, sourceLocale, write.now); err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if requestSavedOG {
		if err := s.legalOG.RequestSaved(ctx, tx, mutation.EntityType, mutation.EntityID, mutation.Locale, false, mutation.EntityType+"_ai_document_saved"); err != nil {
			return legalAIDocumentApplyResult{}, err
		}
	}
	return output, nil
}

// legalAIDocumentWrite is one authorized AI document write.
type legalAIDocumentWrite struct {
	root         legalContentDocumentRoot
	locked       AIDocument
	sourceLocale string
	mutation     AIDocumentMutation
	fence        contentblock.DomainFence
	now          time.Time
}

func (w legalAIDocumentWrite) batch() contentblock.Batch {
	if w.mutation.Content != nil {
		return *w.mutation.Content
	}
	return contentblock.Batch{
		DocumentID: *w.root.ContentDocumentID, ExpectedRevision: uuid.MustParse(w.mutation.ExpectedRevision),
		ContributorMemberIDs: []uuid.UUID{uuid.MustParse(w.mutation.ContributorMemberID)},
	}
}

// applyAITargetLocale creates, edits or deletes the target locale and
// records the locale content change.
func (s *AIDocumentService) applyAITargetLocale(ctx context.Context, tx *gorm.DB, w legalAIDocumentWrite) (legalAIDocumentApplyResult, error) {
	m := w.mutation
	expected := uuid.MustParse(m.ExpectedRevision)
	output := legalAIDocumentApplyResult{CurrentDocumentRevision: w.locked.Revision, applied: m}
	if m.Translation == AITranslationDelete {
		result, err := deleteLegalTargetLocale(ctx, tx, s.contentBlocks, legalTargetLocaleDeleteInput{
			EntityType: m.EntityType, EntityID: m.EntityID, Locale: m.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: m.ExpectedTargetRevision,
			ContributorMemberIDs: []uuid.UUID{uuid.MustParse(m.ContributorMemberID)}, Fence: w.fence,
		})
		if err != nil {
			return legalAIDocumentApplyResult{}, err
		}
		output.Content = result
	} else {
		target, err := applyLegalTargetLocaleMutation(ctx, tx, s.contentBlocks, legalTargetLocaleMutationInput{
			EntityType: m.EntityType, EntityID: m.EntityID, Locale: m.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: m.ExpectedTargetRevision,
			Batch: w.batch(),
			Policy: targetlocale.Policy{
				AllowCreate:        m.Translation == AITranslationCreate,
				SeedSource:         m.Translation == AITranslationCreate,
				AllowLocaleDeletes: m.AuthoritativeTargetReplacement,
			},
			SetTitle: m.SetTitle, Title: m.Title, Now: w.now, Fence: w.fence,
		})
		if err != nil {
			return legalAIDocumentApplyResult{}, err
		}
		output.TargetRevision = &target.TargetRevision
		output.Content = contentblock.Result{
			DocumentRevision: uuid.MustParse(target.DocumentRevision),
			Changed:          target.Changed,
			ChangedLocales:   []string{m.Locale},
		}
	}
	if output.Content.Changed {
		output.localeChange = &legalLocaleContentChange{
			kind: m.EntityType, policyID: m.EntityID, version: w.root.Version, locale: m.Locale,
			operation: legalTargetLocaleContentOperation(m.Translation, w.locked.LocaleExists),
			memberID:  m.ContributorMemberID,
		}
	}
	return output, nil
}

// applyAISourceLocale writes the source Blocks and title as one source
// revision.
func (s *AIDocumentService) applyAISourceLocale(ctx context.Context, tx *gorm.DB, w legalAIDocumentWrite) (legalAIDocumentApplyResult, error) {
	result, err := s.contentBlocks.ApplyBatchWithMetadata(
		ctx, tx, w.batch(), w.fence, s.aiMetadataMutation(w.mutation, w.sourceLocale, w.now),
	)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	return legalAIDocumentApplyResult{Content: result, CurrentDocumentRevision: w.locked.Revision, applied: w.mutation}, nil
}

// output is the result the AI document seam reports.
func (r legalAIDocumentApplyResult) output() AIDocumentMutationResult {
	return AIDocumentMutationResult{
		Revision: r.Content.DocumentRevision.String(), TargetRevision: r.TargetRevision,
		Changed: r.Content.Changed,
	}
}

// audit names the target locale content record of a changed target locale;
// a source write appends none.
func (r legalAIDocumentApplyResult) audit() mutation.Audit {
	if r.localeChange == nil {
		return mutation.Unchanged()
	}
	return r.localeChange.audit()
}

// publish signals the changed document to interactive collaborators.
func (r legalAIDocumentApplyResult) publish(ctx context.Context, tx *gorm.DB) error {
	if !r.Content.Changed {
		return nil
	}
	m := r.applied
	entity := managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS
	if m.EntityType == "privacy" {
		entity = managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY
	}
	return contentsignal.PublishInteractive(ctx, tx, entity, m.EntityID, m.Locale, r.Content.DocumentRevision.String(), r.TargetRevision, true, m.Translation == AITranslationDelete, m.ContributorMemberID)
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
				return effect, domainerr.InvalidField("title", "source title cannot be absent")
			}
			normalized := strings.TrimSpace(*mutation.Title)
			if normalized == "" {
				return effect, domainerr.InvalidField("title", "must not be empty")
			}
			result := tx.WithContext(ctx).Table(mutation.EntityType+"_history").
				Where("id = ? AND title IS DISTINCT FROM ?", mutation.EntityID, normalized).
				Updates(structured.Fields{"title": normalized, "updated_at": now})
			if result.Error != nil {
				return effect, result.Error
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
			return contentblock.DomainContext{}, domainerr.Precondition(lockedState.EntityType + " content document changed; reload before saving")
		}
		return contentblock.DomainContext{
			SourceLocale: sourceLocale,
		}, nil
	}
}

func requireLegalAIPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return nil, domainerr.NoPermission("read", "legal document")
	}
	return principal, nil
}

func validateAIDocumentIdentity(entityType, entityID, locale string) error {
	if entityType != "privacy" && entityType != "terms" {
		return domainerr.InvalidField("entity_type", "must be privacy or terms")
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return domainerr.InvalidField("entity_id", "must be a UUID")
	}
	if _, err := canonicalLegalLocale(locale); err != nil {
		return err
	}
	return nil
}

func (m AIDocumentMutation) String() string {
	return fmt.Sprintf("%s/%s@%s", m.EntityType, m.EntityID, m.Locale)
}

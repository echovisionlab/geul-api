package legal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"

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

// legalAIDocumentApplyResult is what one applied mutation changed.
type legalAIDocumentApplyResult struct {
	Content                 contentblock.Result
	TargetRevision          *string
	CurrentDocumentRevision string
	applied                 AIDocumentMutation
	localeChange            *legalLocaleContentChange
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
	Units   mutation.UnitOfWork[Store] // legalpostgres.NewUnits
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
}

func (d AIDocumentDeps) validate() error {
	return requireDeps("legal AI document dependencies are required: ", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	})
}

// AIDocumentService holds the Legal AI document and translation
// interchange use cases.
type AIDocumentService struct {
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewAIDocumentService validates deps and returns the Legal AI document use
// cases.
func NewAIDocumentService(deps AIDocumentDeps) (*AIDocumentService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &AIDocumentService{spiceDB: deps.SpiceDB, mutations: mutations}, nil
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
	kind, err := legalKind(entityType)
	if err != nil {
		return AIDocument{}, err
	}
	var result AIDocument
	err = readLocked(ctx, s.mutations, mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		if _, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false); err != nil {
			return err
		}
		// A read keeps to View in every lifecycle: an AI document names the caller's
		// own documents, not every archived version an Author may see elsewhere.
		return authorizeLegal(ctx, s.spiceDB, lockActive, kind, entityID, "view", true, legalActionView)
	}), func(ctx context.Context, store Store) error {
		principal := auth.GetUser(ctx)
		if principal == nil || principal.MemberID == "" {
			return domainerr.AuthenticationRequired()
		}
		var err error
		result, _, err = loadAIDocumentAfterAuthorization(ctx, store, kind, entityID, locale, principal.MemberID.String())
		return err
	})
	if err != nil {
		return AIDocument{}, normalizeLegalContentBlockError(entityType, err)
	}
	return result, nil
}

func loadAIDocumentAfterAuthorization(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
	locale string,
	viewerMemberID string,
) (AIDocument, string, error) {
	entityType := kind.entity()
	targetState, err := loadLegalTargetLocaleState(ctx, store, kind, entityID, locale, false)
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

	kind, err := legalKind(entityType)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var root Root
	var memberID string
	var applied legalAIDocumentApplyResult
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[legalAIDocumentApplyResult, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			var err error
			root, memberID, err = s.authorizeAIDocumentEdit(ctx, store, lockActive, kind, entityID)
			return err
		}),
		Change: func(ctx context.Context, store Store) (legalAIDocumentApplyResult, error) {
			var err error
			applied, err = compileAndApplyAIDocumentMutation(
				ctx, store, root, kind, entityID, locale, memberID, compiler, true, false,
			)
			if err == nil && mode == AIDocumentExecutionValidate {
				err = errRollbackLegalAIDocumentValidation
			}
			return applied, err
		},
		Audit: func(result legalAIDocumentApplyResult) mutation.Audit { return result.audit() },
		Outbox: func(ctx context.Context, store Store, result legalAIDocumentApplyResult) error {
			return result.publish(ctx, store)
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
	store Store,
	lockActive LockActive,
	kind *policyKind,
	entityID string,
) (Root, string, error) {
	entityType := kind.entity()
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
	if err != nil {
		return root, "", err
	}
	principal, err := requireLegalAIPrincipal(ctx)
	if err != nil {
		return root, "", legalAIDocumentMutationAccessError(entityType, entityID, err)
	}
	active, err := lockActive(principal)
	if err != nil {
		return root, "", err
	}
	if !active {
		return root, "", domainerr.ResourceNotFound(entityType, entityID)
	}
	if err := requireLegalPermission(ctx, s.spiceDB, kind, entityID, legalMutationAction(kind, root.Status, legalActionEdit)); err != nil {
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
			return &aidocument.RevisionConflict{
				Kind:                    aidocument.ConflictTargetRevision,
				CurrentDocumentRevision: state.Revision,
				CurrentTargetRevision:   currentTargetRevision,
				Message:                 "Legal AI document revision changed; reload before saving",
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

func applyAIDocumentAfterAuthorization(
	ctx context.Context,
	store Store,
	root Root,
	kind *policyKind,
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
		root: root, kind: kind, locked: lockedState, sourceLocale: sourceLocale, mutation: mutation,
		fence: legalAuthorizedAIDocumentFence(root, lockedState, sourceLocale), now: dbtime.Now(),
	}
	var output legalAIDocumentApplyResult
	var err error
	if mutation.Locale != sourceLocale {
		output, err = applyAITargetLocale(ctx, store, write)
	} else {
		output, err = applyAISourceLocale(ctx, store, write)
	}
	if err != nil || !output.Content.Changed {
		return output, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, *root.ContentDocumentID, sourceLocale)
	if err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if err := refreshDerivedContent(ctx, store, kind, mutation.EntityID, snapshot, sourceLocale, write.now); err != nil {
		return legalAIDocumentApplyResult{}, err
	}
	if requestSavedOG {
		if err := store.RequestOG(ctx, kind.kind, mutation.EntityID, mutation.Locale, false, mutation.EntityType+"_ai_document_saved"); err != nil {
			return legalAIDocumentApplyResult{}, err
		}
	}
	return output, nil
}

// legalAIDocumentWrite is one authorized AI document write.
type legalAIDocumentWrite struct {
	root         Root
	kind         *policyKind
	locked       AIDocument
	sourceLocale string
	mutation     AIDocumentMutation
	fence        ContentFence
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
func applyAITargetLocale(ctx context.Context, store Store, w legalAIDocumentWrite) (legalAIDocumentApplyResult, error) {
	m := w.mutation
	expected := uuid.MustParse(m.ExpectedRevision)
	output := legalAIDocumentApplyResult{CurrentDocumentRevision: w.locked.Revision, applied: m}
	if m.Translation == AITranslationDelete {
		result, err := deleteLegalTargetLocale(ctx, store, legalTargetLocaleDeleteInput{
			Kind: w.kind, EntityID: m.EntityID, Locale: m.Locale,
			ExpectedDocumentRevision: expected, ExpectedTargetRevision: m.ExpectedTargetRevision,
			ContributorMemberIDs: []uuid.UUID{uuid.MustParse(m.ContributorMemberID)}, Fence: w.fence,
		})
		if err != nil {
			return legalAIDocumentApplyResult{}, err
		}
		output.Content = result
	} else {
		target, err := applyLegalTargetLocaleMutation(ctx, store, legalTargetLocaleMutationInput{
			Kind: w.kind, EntityID: m.EntityID, Locale: m.Locale,
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
func applyAISourceLocale(ctx context.Context, store Store, w legalAIDocumentWrite) (legalAIDocumentApplyResult, error) {
	result, err := store.ApplyContentBatchWithMetadata(
		ctx, w.batch(), w.fence, aiMetadataMutation(w.kind, w.mutation, w.sourceLocale, w.now),
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
func (r legalAIDocumentApplyResult) publish(ctx context.Context, store Store) error {
	if !r.Content.Changed {
		return nil
	}
	m := r.applied
	kind := Terms
	if m.EntityType == "privacy" {
		kind = Privacy
	}
	return store.PublishContentUpdate(ctx, ContentUpdate{
		Kind: kind, PolicyID: m.EntityID, Locale: m.Locale,
		DocumentRevision: r.Content.DocumentRevision.String(), TargetRevision: r.TargetRevision,
		Deleted: m.Translation == AITranslationDelete, ContributorMemberID: m.ContributorMemberID,
	})
}

func aiMetadataMutation(
	kind *policyKind,
	mutation AIDocumentMutation,
	sourceLocale string,
	now time.Time,
) MetadataEffect {
	return func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
		effect := contentblock.MetadataEffect{}
		if mutation.SetTitle {
			if mutation.Title == nil {
				return effect, domainerr.InvalidField("title", "source title cannot be absent")
			}
			normalized := strings.TrimSpace(*mutation.Title)
			if normalized == "" {
				return effect, domainerr.InvalidField("title", "must not be empty")
			}
			changed, err := store.ReplaceTitle(ctx, kind.kind, mutation.EntityID, normalized, now)
			if err != nil {
				return effect, err
			}
			effect.Changed = changed
			effect.AffectsTranslationSource = effect.Changed
			effect.SourceLocale = sourceLocale
		}
		return effect, nil
	}
}

func legalAuthorizedAIDocumentFence(
	root Root,
	lockedState AIDocument,
	sourceLocale string,
) ContentFence {
	return func(_ context.Context, _ Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
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

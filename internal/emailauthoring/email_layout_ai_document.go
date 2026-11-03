package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/google/uuid"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// EmailLayoutAIDocumentUnit is the Email Layout-owned stable semantic unit
// descriptor consumed by the transport adapter. Target values are present only
// when that locale explicitly owns the unit, including an explicit empty value.
type EmailLayoutAIDocumentUnit struct {
	Handle      string
	Kind        string
	Element     string
	Attribute   string
	Order       int
	SourceValue string
	LocaleValue *string
}

type EmailLayoutAIDocumentState struct {
	LayoutID         string
	DocumentRevision string
	TargetRevision   *string
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	ViewerMemberID   string
	Units            []EmailLayoutAIDocumentUnit
	// SourceMarkup is the source locale's stored wrapper, including the
	// private unit markers that bind translations to source text.
	SourceMarkup string

	sourceHTML      string
	sourceText      string
	documentID      uuid.UUID
	localeUpdatedAt *time.Time
}

type EmailLayoutAIDocumentMutation struct {
	LayoutID                 string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedSource           string
	ExpectedPresence         bool
	ContributorMemberID      uuid.UUID
	Values                   map[string]string
	ReplaceValues            bool
	// ReplaceSourceMarkup stores SourceMarkup as the source locale's wrapper.
	// Every translatable unit must keep its identity, kind and source value so
	// existing translations stay attached; only surrounding markup changes.
	ReplaceSourceMarkup bool
	SourceMarkup        string
	CreateTranslation   bool
	DeleteTranslation   bool
	Noop                bool
}

type EmailLayoutAIDocumentMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

type EmailLayoutAIDocumentExecutionMode uint8

const (
	EmailLayoutAIDocumentExecutionValidate EmailLayoutAIDocumentExecutionMode = iota
	EmailLayoutAIDocumentExecutionApply
)

// EmailLayoutAIDocumentMutationCompiler is invoked only with current
// authorized state loaded under the Email Layout root lock.
type EmailLayoutAIDocumentMutationCompiler func(EmailLayoutAIDocumentState) (EmailLayoutAIDocumentMutation, error)

type emailLayoutAIDocumentCompilerError struct{ cause error }

func (e *emailLayoutAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *emailLayoutAIDocumentCompilerError) Unwrap() error { return e.cause }

// EmailLayoutAIDocumentService owns authorization, active-delivery lifecycle,
// locking, opaque CAS, locale CRUD, and source/target persistence. The adapter
// only converts this typed state to DCDP.
type EmailLayoutAIDocumentService struct{ internal *InternalService }

// LayoutAIDocuments returns the Email Layout AI document use cases.
func (s *InternalService) LayoutAIDocuments() *EmailLayoutAIDocumentService {
	return &EmailLayoutAIDocumentService{internal: s}
}

func (s *EmailLayoutAIDocumentService) Load(
	ctx context.Context,
	layoutID string,
	locale string,
) (EmailLayoutAIDocumentState, error) {
	layoutID, err := emailcontent.CanonicalID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	locale, err = canonicalEmailLayoutRoomLocale(locale)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	var state EmailLayoutAIDocumentState
	err = s.internal.units.ReadLocked(ctx, func(store Store) error {
		memberID, err := s.authorize(ctx, store, layoutID, emailcontent.ShareLock)
		if err != nil {
			return err
		}
		state, err = loadEmailLayoutAIDocumentState(ctx, store, layoutID, locale, memberID, emailcontent.ShareLock)
		return err
	})
	return state, failurecause.WithAttrs(err, slog.String("entity_type", "email_layout"), slog.String("entity_id", layoutID), slog.String("locale", locale))
}

// LoadSource reads the layout in its source locale, which callers editing the
// source wrapper do not need to know in advance.
func (s *EmailLayoutAIDocumentService) LoadSource(ctx context.Context, layoutID string) (EmailLayoutAIDocumentState, error) {
	layoutID, err := emailcontent.CanonicalID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	var state EmailLayoutAIDocumentState
	err = s.internal.units.ReadLocked(ctx, func(store Store) error {
		memberID, err := s.authorize(ctx, store, layoutID, emailcontent.ShareLock)
		if err != nil {
			return err
		}
		authority, err := loadEmailLayoutDocumentAuthority(ctx, store, layoutID, emailcontent.ShareLock)
		if err != nil {
			return err
		}
		state, err = loadEmailLayoutAIDocumentState(ctx, store, layoutID, authority.SourceLocale, memberID, emailcontent.ShareLock)
		return err
	})
	return state, failurecause.WithAttrs(err, slog.String("entity_type", "email_layout"), slog.String("entity_id", layoutID))
}

// authorize locks the layout root with lock, rejects a layout an active
// delivery still renders and authorizes the request's Member to edit it.
func (s *EmailLayoutAIDocumentService) authorize(ctx context.Context, store Store, layoutID string, lock emailcontent.RowLock) (uuid.UUID, error) {
	if err := lockEmailLayoutAIDocumentRoot(ctx, store, layoutID, lock); err != nil {
		return uuid.Nil, err
	}
	if err := store.RequireLayoutMutable(ctx, layoutID); err != nil {
		return uuid.Nil, err
	}
	can, canErr := emailLayoutEditCan(layoutID)
	return emailcontent.RequireEditor(ctx, s.internal.spiceDB, can, canErr, "email_layout_id", "email layout")
}

var errRollbackEmailLayoutAIDocumentValidation = errors.New("rollback Email Layout AI document validation")

// layoutAIDocumentApply is one applied layout AI document mutation and the
// record it names.
type layoutAIDocumentApply struct {
	mutation EmailLayoutAIDocumentMutation
	result   EmailLayoutAIDocumentMutationResult
	record   mutation.Audit
}

// ExecuteAIDocumentMutation is Email Layout's exact DCDP mutation boundary.
// It locks the root and active-delivery lifecycle, performs one Edit decision,
// locks current source/locale/contributor facts, and only then invokes the
// compiler. Validate rolls back the identical persistence path.
func (s *EmailLayoutAIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	layoutID string,
	locale string,
	mode EmailLayoutAIDocumentExecutionMode,
	compiler EmailLayoutAIDocumentMutationCompiler,
) (EmailLayoutAIDocumentMutationResult, error) {
	if compiler == nil {
		return EmailLayoutAIDocumentMutationResult{}, domainerr.DependencyUnavailable("Email Layout AI document compiler")
	}
	if mode != EmailLayoutAIDocumentExecutionValidate && mode != EmailLayoutAIDocumentExecutionApply {
		return EmailLayoutAIDocumentMutationResult{}, domainerr.InvalidField("mode", "is not supported")
	}
	layoutID, err := emailcontent.CanonicalID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutAIDocumentMutationResult{}, err
	}
	locale, err = canonicalEmailLayoutRoomLocale(locale)
	if err != nil {
		return EmailLayoutAIDocumentMutationResult{}, err
	}
	var memberID uuid.UUID
	var applied layoutAIDocumentApply
	_, err = mutation.Run(ctx, s.internal.mutations, mutation.StoreSpec[layoutAIDocumentApply, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, _ mutation.Require) error {
			var err error
			if memberID, err = s.authorize(ctx, store, layoutID, emailcontent.UpdateLock); err != nil {
				return err
			}
			return lockAIDocumentContributor(ctx, store, memberID)
		}),
		Change: func(ctx context.Context, store Store) (layoutAIDocumentApply, error) {
			var err error
			applied, err = compileAndApplyLayoutAIDocument(ctx, store, layoutID, locale, memberID, compiler)
			if err == nil && mode == EmailLayoutAIDocumentExecutionValidate {
				err = errRollbackEmailLayoutAIDocumentValidation
			}
			return applied, err
		},
		Audit: func(applied layoutAIDocumentApply) mutation.Audit { return applied.record },
		Outbox: func(ctx context.Context, store Store, applied layoutAIDocumentApply) error {
			m := applied.mutation
			return store.PublishContentUpdate(ctx, emailcontent.ContentUpdate{
				Entity: "email_layout", ID: m.LayoutID, Locale: m.Locale,
				DocumentRevision: applied.result.DocumentRevision, TargetRevision: applied.result.TargetRevision,
				Changed: applied.result.Changed, Deleted: m.DeleteTranslation, ContributorMemberID: m.ContributorMemberID.String(),
			})
		},
	})
	if errors.Is(err, errRollbackEmailLayoutAIDocumentValidation) {
		return applied.result, nil
	}
	if err != nil {
		var compilerErr *emailLayoutAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			err = compilerErr.cause
		}
		return EmailLayoutAIDocumentMutationResult{}, failurecause.WithAttrs(err, slog.String("entity_type", "email_layout"), slog.String("entity_id", layoutID), slog.String("locale", locale))
	}
	return applied.result, nil
}

// compileAndApplyLayoutAIDocument loads the locked state, invokes the
// compiler with it and applies the validated mutation.
func compileAndApplyLayoutAIDocument(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
	memberID uuid.UUID,
	compiler EmailLayoutAIDocumentMutationCompiler,
) (layoutAIDocumentApply, error) {
	current, err := loadEmailLayoutAIDocumentState(ctx, store, layoutID, locale, memberID, emailcontent.UpdateLock)
	if err != nil {
		return layoutAIDocumentApply{}, err
	}
	compiled, err := compiler(cloneEmailLayoutAIDocumentState(current))
	if err != nil {
		return layoutAIDocumentApply{}, &emailLayoutAIDocumentCompilerError{cause: err}
	}
	compiled, err = validateEmailLayoutAIDocumentMutation(compiled)
	if err != nil {
		return layoutAIDocumentApply{}, err
	}
	if err := validateCompiledEmailLayoutAIDocumentMutation(current, compiled); err != nil {
		return layoutAIDocumentApply{}, err
	}
	return applyLayoutAIDocument(ctx, store, current, compiled)
}

// applyLayoutAIDocument writes the mutation: a created or deleted
// translation, or replaced source or target values. A change records the
// locale content audit.
func applyLayoutAIDocument(
	ctx context.Context,
	store Store,
	current EmailLayoutAIDocumentState,
	m EmailLayoutAIDocumentMutation,
) (layoutAIDocumentApply, error) {
	now := dbtime.Now()
	changed := false
	var err error
	switch {
	case m.CreateTranslation:
		changed, err = true, store.SeedLayoutTranslation(ctx, m.LayoutID, m.Locale, current.SourceLocale, now)
	case m.DeleteTranslation:
		changed, err = true, deleteEmailLayoutAIDocumentTranslation(ctx, store, current, m)
	case m.ReplaceValues:
		changed = !maps.Equal(emailLayoutLocaleValues(current), m.Values)
		if changed {
			err = replaceLayoutAIDocumentValues(ctx, store, current, m, now)
		}
	case m.ReplaceSourceMarkup:
		changed, err = replaceLayoutAIDocumentSourceMarkup(ctx, store, current, m, now)
	}
	if err != nil {
		return layoutAIDocumentApply{}, err
	}
	next, err := loadEmailLayoutAIDocumentState(ctx, store, m.LayoutID, m.Locale, m.ContributorMemberID, emailcontent.UpdateLock)
	if err != nil {
		return layoutAIDocumentApply{}, err
	}
	applied := layoutAIDocumentApply{
		mutation: m, record: mutation.Unchanged(),
		result: EmailLayoutAIDocumentMutationResult{
			DocumentRevision: next.DocumentRevision, TargetRevision: cloneEmailLayoutAIDocumentString(next.TargetRevision), Changed: changed,
		},
	}
	if changed {
		applied.record = emailLayoutLocaleContentChange(
			m.ContributorMemberID.String(), m.LayoutID, m.Locale,
			emailcontent.LocaleContentOperation(m.Locale == current.SourceLocale, m.CreateTranslation, m.DeleteTranslation, m.ExpectedPresence),
		)
	}
	return applied, nil
}

func deleteEmailLayoutAIDocumentTranslation(ctx context.Context, store Store, current EmailLayoutAIDocumentState, m EmailLayoutAIDocumentMutation) error {
	deleted, err := store.DeleteLayoutTranslation(ctx, m.LayoutID, m.Locale)
	if err != nil {
		return err
	}
	if !deleted {
		return emailLayoutAIDocumentConflict(aidocument.ConflictTargetRevision, current)
	}
	return nil
}

// replaceLayoutAIDocumentValues stores the mutation's values as the source
// locale's next document revision or as the target locale's row.
func replaceLayoutAIDocumentValues(ctx context.Context, store Store, current EmailLayoutAIDocumentState, m EmailLayoutAIDocumentMutation, now time.Time) error {
	if m.Locale != current.SourceLocale {
		_, err := saveEmailLayoutTarget(ctx, store, m.LayoutID, m.Locale, current.sourceHTML, m.Values, now, dereferenceLayoutTime(current.localeUpdatedAt))
		return err
	}
	contentHTML, contentText, err := emailutil.ApplyLayoutSourceValues(current.sourceHTML, m.Values)
	if err != nil {
		return domainerr.InvalidField("content", err.Error())
	}
	expectedRevision, err := uuid.Parse(current.DocumentRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return fmt.Errorf("Email Layout Content Document revision %q is invalid", current.DocumentRevision)
	}
	authority := LayoutAuthority{LayoutID: m.LayoutID, SourceLocale: current.SourceLocale, DocumentID: current.documentID}
	_, err = saveEmailLayoutSource(
		ctx, store, m.LayoutID, authority, expectedRevision,
		&emailutil.LayoutTranslationDocument{ContentHTML: contentHTML, ContentText: contentText}, now,
	)
	return err
}

// replaceLayoutAIDocumentSourceMarkup stores new source wrapper markup as the
// next document revision. It canonicalizes and validates the markup like an
// editor save and rejects any change to the translatable units.
func replaceLayoutAIDocumentSourceMarkup(ctx context.Context, store Store, current EmailLayoutAIDocumentState, m EmailLayoutAIDocumentMutation, now time.Time) (bool, error) {
	contentHTML, err := emailutil.CanonicalizeLayoutSourceMarkers(m.SourceMarkup)
	if err != nil {
		return false, domainerr.InvalidField("source_markup", err.Error())
	}
	if err := emailutil.ValidateLayoutHTMLContentError(contentHTML); err != nil {
		return false, domainerr.InvalidField("source_markup", err.Error())
	}
	units, err := emailutil.ExtractLayoutContentUnits(contentHTML)
	if err != nil {
		return false, domainerr.InvalidField("source_markup", err.Error())
	}
	if !sameEmailLayoutSourceUnits(current.Units, units) {
		return false, domainerr.InvalidField(
			"source_markup",
			"must keep every translatable unit marker, element, attribute and source value unchanged; edit text with document tools",
		)
	}
	if contentHTML == current.sourceHTML {
		return false, nil
	}
	expectedRevision, err := uuid.Parse(current.DocumentRevision)
	if err != nil || expectedRevision == uuid.Nil {
		return false, fmt.Errorf("Email Layout Content Document revision %q is invalid", current.DocumentRevision)
	}
	contentText := current.sourceText
	authority := LayoutAuthority{LayoutID: m.LayoutID, SourceLocale: current.SourceLocale, DocumentID: current.documentID}
	_, err = saveEmailLayoutSource(
		ctx, store, m.LayoutID, authority, expectedRevision,
		&emailutil.LayoutTranslationDocument{ContentHTML: &contentHTML, ContentText: &contentText}, now,
	)
	return err == nil, err
}

func sameEmailLayoutSourceUnits(current []EmailLayoutAIDocumentUnit, next []emailutil.LayoutContentUnit) bool {
	if len(current) != len(next) {
		return false
	}
	for index, unit := range current {
		candidate := next[index]
		if unit.Handle != candidate.Handle || unit.Kind != candidate.Kind || unit.Element != candidate.Element ||
			unit.Attribute != candidate.Attribute || unit.SourceValue != candidate.SourceValue {
			return false
		}
	}
	return true
}

func lockEmailLayoutAIDocumentRoot(ctx context.Context, store Store, layoutID string, lock emailcontent.RowLock) error {
	if err := store.LockLayoutRoot(ctx, layoutID, lock); err != nil {
		if errors.Is(err, ErrLayoutNotFound) {
			return domainerr.ResourceNotFound("email layout", layoutID)
		}
		return err
	}
	return nil
}

func loadEmailLayoutAIDocumentState(
	ctx context.Context,
	store Store,
	layoutID string,
	locale string,
	memberID uuid.UUID,
	lock emailcontent.RowLock,
) (EmailLayoutAIDocumentState, error) {
	authority, err := loadEmailLayoutDocumentAuthority(ctx, store, layoutID, lock)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	source, err := store.CanonicalLayoutSource(ctx, layoutID, authority.SourceLocale)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	sourceHTML := derefString(source.ContentHTML)
	descriptors, err := emailutil.ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return EmailLayoutAIDocumentState{}, domainerr.Precondition("Email Layout source unit markers require backfill before editing").WithCause(failurecause.ServerFault(err))
	}

	localeEntry, err := store.LayoutTranslationEntry(ctx, layoutID, locale)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	localeExists := localeEntry != nil
	values := make(map[string]string)
	var localeUpdatedAt *time.Time
	if localeEntry != nil {
		updated := localeEntry.UpdatedAt
		localeUpdatedAt = &updated
		if locale == authority.SourceLocale {
			for _, descriptor := range descriptors {
				values[descriptor.Handle] = descriptor.SourceValue
			}
		} else if localeEntry.ContentHTML != nil {
			values, err = emailutil.ExtractLayoutStoredLocaleValues(*localeEntry.ContentHTML)
			if err != nil {
				return EmailLayoutAIDocumentState{}, domainerr.Precondition("Email Layout target unit markers require backfill before editing").WithCause(failurecause.ServerFault(err))
			}
		}
	}
	if locale == authority.SourceLocale && !localeExists {
		return EmailLayoutAIDocumentState{}, domainerr.Precondition("Email Layout source locale row is missing")
	}

	state := EmailLayoutAIDocumentState{
		LayoutID: layoutID, DocumentRevision: authority.DocumentRevision.String(),
		SourceLocale: authority.SourceLocale, Locale: locale,
		LocaleExists: localeExists, ViewerMemberID: memberID.String(),
		SourceMarkup: sourceHTML, sourceHTML: sourceHTML, sourceText: derefString(source.ContentText), documentID: authority.DocumentID,
		localeUpdatedAt: localeUpdatedAt,
	}
	for _, descriptor := range descriptors {
		unit := EmailLayoutAIDocumentUnit{
			Handle: descriptor.Handle, Kind: descriptor.Kind, Element: descriptor.Element,
			Attribute: descriptor.Attribute, Order: descriptor.Order, SourceValue: descriptor.SourceValue,
		}
		if value, exists := values[descriptor.Handle]; exists {
			copy := value
			unit.LocaleValue = &copy
		}
		state.Units = append(state.Units, unit)
	}
	state.TargetRevision, err = deriveEmailLayoutAIDocumentTargetRevision(state)
	return state, err
}

func validateCompiledEmailLayoutAIDocumentMutation(
	state EmailLayoutAIDocumentState,
	mutation EmailLayoutAIDocumentMutation,
) error {
	if mutation.LayoutID != state.LayoutID || mutation.Locale != state.Locale ||
		mutation.ContributorMemberID.String() != state.ViewerMemberID {
		return domainerr.InvalidField(
			"mutation",
			"compiled Email Layout identity, locale, and contributor must match the locked state",
		)
	}
	if mutation.ExpectedDocumentRevision != state.DocumentRevision || mutation.ExpectedSource != state.SourceLocale {
		return emailLayoutAIDocumentConflict(aidocument.ConflictDocumentRevision, state)
	}
	if mutation.ExpectedPresence != state.LocaleExists ||
		!emailLayoutAIDocumentStringEqual(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return emailLayoutAIDocumentConflict(aidocument.ConflictTargetRevision, state)
	}
	return nil
}

func validateEmailLayoutAIDocumentMutation(
	input EmailLayoutAIDocumentMutation,
) (EmailLayoutAIDocumentMutation, error) {
	layoutID, err := emailcontent.CanonicalID("email_layout_id", input.LayoutID)
	if err != nil {
		return input, err
	}
	input.LayoutID = layoutID
	input.Locale, err = canonicalEmailLayoutRoomLocale(input.Locale)
	if err != nil {
		return input, err
	}
	input.ExpectedSource, err = canonicalEmailLayoutRoomLocale(input.ExpectedSource)
	if err != nil {
		return input, err
	}
	input.ExpectedDocumentRevision = strings.TrimSpace(input.ExpectedDocumentRevision)
	if input.Locale == "" || input.ExpectedSource == "" || input.ExpectedDocumentRevision == "" {
		return input, domainerr.InvalidField(
			"document_revision",
			"locale, source locale, and document revision are required",
		)
	}
	if input.ExpectedTargetRevision != nil {
		revision := strings.TrimSpace(*input.ExpectedTargetRevision)
		if revision == "" {
			return input, domainerr.InvalidField("target_revision", "must not be empty")
		}
		input.ExpectedTargetRevision = &revision
	}
	modes := 0
	for _, enabled := range []bool{input.ReplaceValues, input.ReplaceSourceMarkup, input.CreateTranslation, input.DeleteTranslation, input.Noop} {
		if enabled {
			modes++
		}
	}
	if modes != 1 {
		return input, domainerr.InvalidField("operation", "exactly one Email Layout AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return input, domainerr.InvalidField("locale", "only a missing non-source Email Layout translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return input, domainerr.InvalidField("locale", "only an existing non-source Email Layout translation can be deleted")
	}
	if input.ReplaceSourceMarkup && (input.Locale != input.ExpectedSource || strings.TrimSpace(input.SourceMarkup) == "") {
		return input, domainerr.InvalidField("source_markup", "only non-empty source locale markup can replace the Email Layout wrapper")
	}
	input.Values = maps.Clone(input.Values)
	return input, nil
}

func deriveEmailLayoutAIDocumentTargetRevision(state EmailLayoutAIDocumentState) (*string, error) {
	if state.Locale == state.SourceLocale || !state.LocaleExists {
		return nil, nil
	}
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: state.DocumentRevision,
		LocaleUpdatedAt: state.localeUpdatedAt,
	})
	if err != nil {
		return nil, err
	}
	return &revision, nil
}

func emailLayoutAIDocumentConflict(
	kind aidocument.ConflictCode,
	state EmailLayoutAIDocumentState,
) *aidocument.RevisionConflict {
	return &aidocument.RevisionConflict{
		Kind:                    kind,
		CurrentDocumentRevision: state.DocumentRevision,
		CurrentTargetRevision:   cloneEmailLayoutAIDocumentString(state.TargetRevision),
		Message:                 fmt.Sprintf("Email Layout AI document revision conflict: current document revision is %q", state.DocumentRevision),
	}
}

func emailLayoutAIDocumentStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneEmailLayoutAIDocumentString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func emailLayoutLocaleValues(state EmailLayoutAIDocumentState) map[string]string {
	values := make(map[string]string)
	for _, unit := range state.Units {
		if unit.LocaleValue != nil {
			values[unit.Handle] = *unit.LocaleValue
		}
	}
	return values
}

func cloneEmailLayoutAIDocumentState(state EmailLayoutAIDocumentState) EmailLayoutAIDocumentState {
	cloned := state
	cloned.TargetRevision = cloneEmailLayoutAIDocumentString(state.TargetRevision)
	cloned.Units = append([]EmailLayoutAIDocumentUnit(nil), state.Units...)
	for index := range cloned.Units {
		if state.Units[index].LocaleValue == nil {
			continue
		}
		value := *state.Units[index].LocaleValue
		cloned.Units[index].LocaleValue = &value
	}
	if state.localeUpdatedAt != nil {
		value := *state.localeUpdatedAt
		cloned.localeUpdatedAt = &value
	}
	return cloned
}

func dereferenceLayoutTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

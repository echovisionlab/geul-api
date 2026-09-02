package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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

	sourceHTML      string
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
	CreateTranslation        bool
	DeleteTranslation        bool
	Noop                     bool
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

type EmailLayoutAIDocumentRevisionConflictKind string

const (
	EmailLayoutAIDocumentDocumentRevisionConflict EmailLayoutAIDocumentRevisionConflictKind = "document_revision"
	EmailLayoutAIDocumentTargetRevisionConflict   EmailLayoutAIDocumentRevisionConflictKind = "target_revision"
)

type EmailLayoutAIDocumentRevisionConflictError struct {
	Kind                    EmailLayoutAIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *EmailLayoutAIDocumentRevisionConflictError) Error() string {
	return fmt.Sprintf(
		"Email Layout AI document revision conflict: current document revision is %q",
		e.CurrentDocumentRevision,
	)
}

// EmailLayoutAIDocumentService owns authorization, active-delivery lifecycle,
// locking, opaque CAS, locale CRUD, and source/target persistence. The adapter
// only converts this typed state to DCDP.
type EmailLayoutAIDocumentService struct{ layouts *EmailLayoutService }

func NewEmailLayoutAIDocumentService(layouts *EmailLayoutService) (*EmailLayoutAIDocumentService, error) {
	if layouts == nil || layouts.db == nil || layouts.spiceDB == nil ||
		layouts.references == nil || layouts.contentBlocks == nil || layouts.auditWriter == nil {
		return nil, errors.New("email layout AI document dependencies are required")
	}
	return &EmailLayoutAIDocumentService{layouts: layouts}, nil
}

func (s *EmailLayoutAIDocumentService) Load(
	ctx context.Context,
	layoutID string,
	locale string,
) (EmailLayoutAIDocumentState, error) {
	layoutID, err := canonicalEmailAIDocumentID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	locale, err = canonicalEmailLayoutRoomLocale(locale)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}

	var state EmailLayoutAIDocumentState
	err = s.layouts.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEmailLayoutAIDocumentRoot(ctx, tx, layoutID, "SHARE"); err != nil {
			return err
		}
		if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, s.layouts.references, layoutID); err != nil {
			return err
		}
		memberID, err := requireEmailLayoutAIDocumentAuthority(ctx, s.layouts.spiceDB, layoutID)
		if err != nil {
			return err
		}
		state, err = s.loadStateAfterAuthorization(ctx, tx, layoutID, locale, memberID, "SHARE")
		return err
	})
	return state, err
}

var errRollbackEmailLayoutAIDocumentValidation = errors.New("rollback Email Layout AI document validation")

// ExecuteAIDocumentMutation is Email Layout's exact DCDP mutation boundary.
// It locks the root and active-delivery lifecycle, performs one Edit decision,
// locks current source/locale/contributor facts, and only then invokes the
// compiler. Validate rolls back the identical persistence and Audit path.
func (s *EmailLayoutAIDocumentService) ExecuteAIDocumentMutation(
	ctx context.Context,
	layoutID string,
	locale string,
	mode EmailLayoutAIDocumentExecutionMode,
	compiler EmailLayoutAIDocumentMutationCompiler,
) (EmailLayoutAIDocumentMutationResult, error) {
	if s == nil || s.layouts == nil || s.layouts.db == nil ||
		s.layouts.spiceDB == nil || s.layouts.references == nil ||
		s.layouts.contentBlocks == nil || s.layouts.auditWriter == nil {
		return EmailLayoutAIDocumentMutationResult{}, errs.DependencyUnavailable("Email Layout AI document")
	}
	if compiler == nil {
		return EmailLayoutAIDocumentMutationResult{}, errs.DependencyUnavailable("Email Layout AI document compiler")
	}
	if mode != EmailLayoutAIDocumentExecutionValidate && mode != EmailLayoutAIDocumentExecutionApply {
		return EmailLayoutAIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	layoutID, err := canonicalEmailAIDocumentID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutAIDocumentMutationResult{}, err
	}
	locale, err = canonicalEmailLayoutRoomLocale(locale)
	if err != nil {
		return EmailLayoutAIDocumentMutationResult{}, err
	}

	var output EmailLayoutAIDocumentMutationResult
	err = s.layouts.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEmailLayoutAIDocumentRoot(ctx, tx, layoutID, "UPDATE"); err != nil {
			return err
		}
		if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, s.layouts.references, layoutID); err != nil {
			return err
		}
		memberID, err := requireEmailLayoutAIDocumentAuthority(ctx, s.layouts.spiceDB, layoutID)
		if err != nil {
			return err
		}
		if err := lockEmailAIDocumentContributor(ctx, tx, memberID); err != nil {
			return err
		}
		current, err := s.loadStateAfterAuthorization(ctx, tx, layoutID, locale, memberID, "UPDATE")
		if err != nil {
			return err
		}
		mutation, err := compiler(cloneEmailLayoutAIDocumentState(current))
		if err != nil {
			return &emailLayoutAIDocumentCompilerError{cause: err}
		}
		mutation, err = validateEmailLayoutAIDocumentMutation(mutation)
		if err != nil {
			return err
		}
		if err := validateCompiledEmailLayoutAIDocumentMutation(current, mutation); err != nil {
			return err
		}
		output, err = s.applyAIDocumentMutationInTransaction(ctx, tx, current, mutation)
		if err != nil {
			return err
		}
		if mode == EmailLayoutAIDocumentExecutionValidate {
			return errRollbackEmailLayoutAIDocumentValidation
		}
		return nil
	})
	if errors.Is(err, errRollbackEmailLayoutAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compilerErr *emailLayoutAIDocumentCompilerError
		if errors.As(err, &compilerErr) {
			return EmailLayoutAIDocumentMutationResult{}, compilerErr.cause
		}
		return EmailLayoutAIDocumentMutationResult{}, err
	}
	return output, nil
}

func (s *EmailLayoutAIDocumentService) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	current EmailLayoutAIDocumentState,
	mutation EmailLayoutAIDocumentMutation,
) (EmailLayoutAIDocumentMutationResult, error) {
	now := time.Now().UTC()
	changed := false
	switch {
	case mutation.CreateTranslation:
		if err := emailutil.SeedLayoutTranslationEntryFromSource(
			ctx, tx, mutation.LayoutID, mutation.Locale, current.SourceLocale, now,
		); err != nil {
			return EmailLayoutAIDocumentMutationResult{}, err
		}
		changed = true
	case mutation.DeleteTranslation:
		result := tx.WithContext(ctx).Exec(
			"DELETE FROM email_layout_translation WHERE entity_id = ? AND locale = ?",
			mutation.LayoutID, mutation.Locale,
		)
		if result.Error != nil {
			return EmailLayoutAIDocumentMutationResult{}, errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return EmailLayoutAIDocumentMutationResult{}, emailLayoutAIDocumentConflict(
				EmailLayoutAIDocumentTargetRevisionConflict,
				current,
			)
		}
		changed = true
	case mutation.ReplaceValues:
		currentValues := emailLayoutLocaleValues(current)
		changed = !maps.Equal(currentValues, mutation.Values)
		if changed {
			if mutation.Locale == current.SourceLocale {
				contentHTML, contentText, err := emailutil.ApplyLayoutSourceValues(current.sourceHTML, mutation.Values)
				if err != nil {
					return EmailLayoutAIDocumentMutationResult{}, errs.InvalidArgument("content", err.Error())
				}
				expectedRevision, err := uuid.Parse(current.DocumentRevision)
				if err != nil || expectedRevision == uuid.Nil {
					return EmailLayoutAIDocumentMutationResult{}, errs.InternalMsg("Email Layout Content Document revision is invalid")
				}
				if _, err := s.layouts.contentBlocks.AdvanceRevision(
					ctx, tx,
					contentblock.AdvanceInput{DocumentID: current.documentID, ExpectedRevision: expectedRevision},
					emailLayoutContentFence(s.layouts.references, mutation.LayoutID),
					func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
						if err := emailutil.SaveLayoutSourceLocaleDocument(
							ctx, tx, mutation.LayoutID, current.SourceLocale,
							emailutil.LayoutTranslationDocument{ContentHTML: contentHTML, ContentText: contentText}, now,
						); err != nil {
							return contentblock.MetadataEffect{}, err
						}
						return contentblock.MetadataEffect{
							Changed: true, AffectsTranslationSource: true,
							ChangedLocales: []string{current.SourceLocale},
						}, nil
					},
				); err != nil {
					return EmailLayoutAIDocumentMutationResult{}, err
				}
			} else {
				contentHTML, contentText, err := emailutil.ApplyLayoutLocaleValues(current.sourceHTML, mutation.Values)
				if err != nil {
					return EmailLayoutAIDocumentMutationResult{}, errs.InvalidArgument("content", err.Error())
				}
				updatedAt := translation.NextTargetUpdatedAt(now, dereferenceLayoutTime(current.localeUpdatedAt))
				if err := emailutil.UpsertLayoutTranslationEntry(
					ctx, tx, mutation.LayoutID, mutation.Locale,
					translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
				); err != nil {
					return EmailLayoutAIDocumentMutationResult{}, err
				}
			}
		}
	case mutation.Noop:
	}

	if changed {
		if err := appendEmailLayoutLocaleContentAudit(
			ctx, tx, s.layouts.auditWriter, mutation.ContributorMemberID.String(),
			mutation.LayoutID, mutation.Locale,
			emailAuthoringLocaleContentOperation(
				mutation.Locale == current.SourceLocale,
				mutation.CreateTranslation, mutation.DeleteTranslation, mutation.ExpectedPresence,
			),
		); err != nil {
			return EmailLayoutAIDocumentMutationResult{}, err
		}
	}
	next, err := s.loadStateAfterAuthorization(
		ctx, tx, mutation.LayoutID, mutation.Locale, mutation.ContributorMemberID, "UPDATE",
	)
	if err != nil {
		return EmailLayoutAIDocumentMutationResult{}, err
	}
	return EmailLayoutAIDocumentMutationResult{
		DocumentRevision: next.DocumentRevision,
		TargetRevision:   cloneEmailLayoutAIDocumentString(next.TargetRevision),
		Changed:          changed,
	}, nil
}

func lockEmailLayoutAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
	lock string,
) error {
	var root emailLayoutBaseRow
	query := tx.WithContext(ctx).Table("email_layout").Where("id = ?", layoutID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("email layout", layoutID)
		}
		return errs.Internal(err)
	}
	return nil
}

func (s *EmailLayoutAIDocumentService) loadStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
	locale string,
	memberID uuid.UUID,
	lock string,
) (EmailLayoutAIDocumentState, error) {
	authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, layoutID, lock)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	source, err := emailutil.LoadCanonicalLayoutTranslationDocument(
		ctx, tx, layoutID, authority.SourceLocale,
	)
	if err != nil {
		return EmailLayoutAIDocumentState{}, err
	}
	sourceHTML := derefString(source.ContentHTML)
	descriptors, err := emailutil.ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return EmailLayoutAIDocumentState{}, errs.FailedPrecondition("Email Layout source unit markers require backfill before editing")
	}

	localeEntry, err := emailutil.LoadLayoutTranslationEntry(ctx, tx, layoutID, locale)
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
				return EmailLayoutAIDocumentState{}, errs.FailedPrecondition("Email Layout target unit markers require backfill before editing")
			}
		}
	}
	if locale == authority.SourceLocale && !localeExists {
		return EmailLayoutAIDocumentState{}, errs.FailedPrecondition("Email Layout source locale row is missing")
	}

	state := EmailLayoutAIDocumentState{
		LayoutID: layoutID, DocumentRevision: authority.DocumentRevision.String(),
		SourceLocale: authority.SourceLocale, Locale: locale,
		LocaleExists: localeExists, ViewerMemberID: memberID.String(),
		sourceHTML: sourceHTML, documentID: authority.DocumentID,
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
	if err != nil {
		return EmailLayoutAIDocumentState{}, errs.Internal(err)
	}
	return state, nil
}

func validateCompiledEmailLayoutAIDocumentMutation(
	state EmailLayoutAIDocumentState,
	mutation EmailLayoutAIDocumentMutation,
) error {
	if mutation.LayoutID != state.LayoutID || mutation.Locale != state.Locale ||
		mutation.ContributorMemberID.String() != state.ViewerMemberID {
		return errs.InvalidArgument(
			"mutation",
			"compiled Email Layout identity, locale, and contributor must match the locked state",
		)
	}
	if mutation.ExpectedDocumentRevision != state.DocumentRevision || mutation.ExpectedSource != state.SourceLocale {
		return emailLayoutAIDocumentConflict(
			EmailLayoutAIDocumentDocumentRevisionConflict,
			state,
		)
	}
	if mutation.ExpectedPresence != state.LocaleExists ||
		!emailLayoutAIDocumentStringEqual(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return emailLayoutAIDocumentConflict(
			EmailLayoutAIDocumentTargetRevisionConflict,
			state,
		)
	}
	return nil
}

func validateEmailLayoutAIDocumentMutation(
	input EmailLayoutAIDocumentMutation,
) (EmailLayoutAIDocumentMutation, error) {
	layoutID, err := canonicalEmailAIDocumentID("email_layout_id", input.LayoutID)
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
		return input, errs.InvalidArgument(
			"document_revision",
			"locale, source locale, and document revision are required",
		)
	}
	if input.ExpectedTargetRevision != nil {
		revision := strings.TrimSpace(*input.ExpectedTargetRevision)
		if revision == "" {
			return input, errs.InvalidArgument("target_revision", "must not be empty")
		}
		input.ExpectedTargetRevision = &revision
	}
	modes := 0
	for _, enabled := range []bool{input.ReplaceValues, input.CreateTranslation, input.DeleteTranslation, input.Noop} {
		if enabled {
			modes++
		}
	}
	if modes != 1 {
		return input, errs.InvalidArgument("operation", "exactly one Email Layout AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return input, errs.InvalidArgument("locale", "only a missing non-source Email Layout translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return input, errs.InvalidArgument("locale", "only an existing non-source Email Layout translation can be deleted")
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
	kind EmailLayoutAIDocumentRevisionConflictKind,
	state EmailLayoutAIDocumentState,
) *EmailLayoutAIDocumentRevisionConflictError {
	return &EmailLayoutAIDocumentRevisionConflictError{
		Kind: kind, CurrentDocumentRevision: state.DocumentRevision,
		CurrentTargetRevision: cloneEmailLayoutAIDocumentString(state.TargetRevision),
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

func requireEmailLayoutAIDocumentAuthority(
	ctx context.Context,
	checker *auth.SpiceDBClient,
	layoutID string,
) (uuid.UUID, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return uuid.Nil, errs.NoPermission("edit", "email layout")
	}
	memberID, err := uuid.Parse(principal.MemberID.String())
	if err != nil || memberID == uuid.Nil || memberID.String() != principal.MemberID.String() {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	can, err := emailLayoutEditCan(layoutID)
	if err != nil {
		return uuid.Nil, errs.InvalidArgument("email_layout_id", "must be a canonical UUID")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return uuid.Nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return uuid.Nil, errs.NoPermission("edit", "email layout")
	}
	return memberID, nil
}

func dereferenceLayoutTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

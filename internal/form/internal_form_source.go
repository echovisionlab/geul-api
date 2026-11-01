package form

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SaveFormDocumentCommand saves one Form room's canonical locale state: the
// locale's title and schema, the revisions the room loaded, the locale values
// it holds and its one origin Member.
type SaveFormDocumentCommand struct {
	FormID                   string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ContributorMemberIDs     []string
	Meta                     *intrav1.FormMeta
	PresentLocaleValues      []*managev1.AIDocumentFieldTarget
}

// SaveFormDocument persists only the Form room's canonical locale product
// state. The Y.Doc passed between Collab replicas is transient and is never a
// storage or semantic-change authority. editor-collab calls as itself: the
// origin Member's current permission authorizes the save under the Form root
// lock.
func (s *Service) SaveFormDocument(ctx context.Context, command SaveFormDocumentCommand) (_ *intrav1.SaveFormDocumentResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("form_id", command.FormID))
	save, err := parseFormDocumentSave(command)
	if err != nil {
		return nil, err
	}
	now := dbtime.Now()
	var root formAIDocumentRoot
	var contributorMemberID string
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[formCollaborativeSaveResult]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			root, err = lockFormDocumentRoot(ctx, tx, save)
			return err
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			if contributorMemberID, err = canonicalFormCollaborationContributor(save.contributorMemberIDs); err != nil {
				return err
			}
			return s.collaboration.RequireCurrentContributor(ctx, tx, save.formID, contributorMemberID)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (formCollaborativeSaveResult, error) {
			return s.saveFormDocument(ctx, tx, root, save, now)
		},
		Audit: func(saved formCollaborativeSaveResult) mutation.Audit {
			if !saved.changed {
				return mutation.Unchanged()
			}
			return mutation.MemberAudit(contributorMemberID, sharedtelemetry.AuditFormUpdated,
				formLocaleContentRecord(save.formID, save.locale, sharedtelemetry.AuditItemOperationUpdated))
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, saved formCollaborativeSaveResult) error {
			if !saved.changed || !saved.titleChanged {
				return nil
			}
			_, err := s.og.RequestAfterMutation(ctx, tx, save.formID, save.locale, save.locale == root.SourceLocale, "form_document_saved")
			return err
		},
	})
	if err != nil {
		err = normalizeFormDocumentSaveError(err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("form", save.formID).WithCause(err)
		}
		return nil, err
	}
	return &intrav1.SaveFormDocumentResponse{
		Success:          true,
		Locale:           result.locale,
		DocumentRevision: result.documentRevision,
		TargetRevision:   result.targetRevision,
	}, nil
}

// formDocumentSave is a validated SaveFormDocumentCommand.
type formDocumentSave struct {
	formID                   string
	locale                   string
	expectedDocumentRevision uuid.UUID
	expectedTargetRevision   *string
	contributorMemberIDs     []string
	title                    *string
	schema                   []byte
}

func parseFormDocumentSave(command SaveFormDocumentCommand) (formDocumentSave, error) {
	save := formDocumentSave{
		formID:                 command.FormID,
		locale:                 strings.TrimSpace(command.Locale),
		expectedTargetRevision: command.ExpectedTargetRevision,
		contributorMemberIDs:   command.ContributorMemberIDs,
	}
	if save.locale == "" {
		return save, domainerr.Required("locale")
	}
	var err error
	save.expectedDocumentRevision, err = uuid.Parse(strings.TrimSpace(command.ExpectedDocumentRevision))
	if err != nil || save.expectedDocumentRevision == uuid.Nil || save.expectedDocumentRevision.String() != command.ExpectedDocumentRevision {
		return save, domainerr.InvalidField("expected_document_revision", "must be a canonical UUID")
	}
	save.title, save.schema, err = formCanonicalDocumentFromMeta(command.Meta)
	if err != nil {
		return save, domainerr.InvalidField("meta", err.Error())
	}
	if err := validateFormCanonicalLocalePresence(save.title, save.schema, command.PresentLocaleValues); err != nil {
		return save, domainerr.InvalidField("present_locale_values", err.Error())
	}
	return save, nil
}

// lockFormDocumentRoot locks the Form root of a collaboration save and
// rejects a save of another Content Document revision before the contributor
// is authorized.
func lockFormDocumentRoot(ctx context.Context, tx *gorm.DB, save formDocumentSave) (formAIDocumentRoot, error) {
	root, err := loadFormAIDocumentRoot(ctx, tx, save.formID, clause.LockingStrengthUpdate)
	if err != nil {
		return formAIDocumentRoot{}, err
	}
	if root.DocumentRevision != save.expectedDocumentRevision.String() {
		return formAIDocumentRoot{}, formDocumentRevisionChangedConflict(root.DocumentRevision)
	}
	return root, nil
}

func (s *Service) saveFormDocument(
	ctx context.Context,
	tx *gorm.DB,
	root formAIDocumentRoot,
	save formDocumentSave,
	now time.Time,
) (formCollaborativeSaveResult, error) {
	current, exists, err := loadFormAIDocumentLocale(ctx, tx, save.formID, save.locale, true)
	if err != nil {
		return formCollaborativeSaveResult{}, err
	}
	if !exists {
		return formCollaborativeSaveResult{}, domainerr.Precondition("Form locale document does not exist")
	}
	isSource := save.locale == root.SourceLocale
	writeNow := now
	if isSource {
		if save.expectedTargetRevision != nil {
			return formCollaborativeSaveResult{}, domainerr.InvalidField("expected_target_revision", "source locale has no target revision")
		}
	} else {
		if err := validateFormTargetSave(ctx, tx, root, current, save); err != nil {
			return formCollaborativeSaveResult{}, err
		}
		writeNow = translation.NextTargetUpdatedAt(writeNow, current.UpdatedAt)
	}

	update, titleChanged := formCollaborativeLocaleUpdates(current, save.title, save.schema, writeNow)
	changed, documentRevision, err := s.writeFormLocale(ctx, tx, root, save, update, writeNow)
	if err != nil {
		return formCollaborativeSaveResult{}, err
	}
	result := formCollaborativeSaveResult{
		locale: save.locale, documentRevision: documentRevision, changed: changed, titleChanged: titleChanged,
	}
	if !isSource {
		result.targetRevision, err = savedFormTargetRevision(ctx, tx, save, documentRevision)
		if err != nil {
			return formCollaborativeSaveResult{}, err
		}
	}
	return result, nil
}

// validateFormTargetSave checks a target locale save against the caller's
// target revision and the source schema it translates.
func validateFormTargetSave(
	ctx context.Context,
	tx *gorm.DB,
	root formAIDocumentRoot,
	current formAIDocumentLocaleRow,
	save formDocumentSave,
) error {
	currentTargetRevision, err := deriveFormTargetRevision(root.DocumentRevision, current)
	if err != nil {
		return err
	}
	if err := translation.ValidateExpectedTargetRevision(
		save.expectedTargetRevision, currentTargetRevision, true,
	); err != nil {
		return translation.WithDocumentRevision(err, root.DocumentRevision)
	}
	source, sourceExists, err := loadFormAIDocumentLocale(ctx, tx, save.formID, root.SourceLocale, false)
	if err != nil {
		return err
	}
	if !sourceExists {
		return domainerr.Precondition("Form source locale document is missing")
	}
	if err := validateFormAIDocumentTargetSchema(source.Schema, save.schema); err != nil {
		return domainerr.InvalidField("meta.schema", err.Error())
	}
	return nil
}

// writeFormLocale applies update to the saved locale. A source locale write
// advances the Content Document revision; a target write leaves it as is.
func (s *Service) writeFormLocale(
	ctx context.Context,
	tx *gorm.DB,
	root formAIDocumentRoot,
	save formDocumentSave,
	update formCollaborativeLocaleUpdate,
	now time.Time,
) (changed bool, documentRevision string, err error) {
	apply := func(ctx context.Context, tx *gorm.DB) (bool, error) {
		return applyFormCollaborativeLocaleUpdates(ctx, tx, save.formID, save.locale, update, now)
	}
	if save.locale != root.SourceLocale {
		changed, err = apply(ctx, tx)
		return changed, root.DocumentRevision, err
	}
	advance, err := advanceFormSourceLocale(ctx, tx, s.contentBlocks, root.DocumentID, save.expectedDocumentRevision, root.SourceLocale, apply)
	if err != nil {
		return false, "", err
	}
	return advance.Changed, advance.DocumentRevision.String(), nil
}

// advanceFormSourceLocale runs apply, a write of the Form source locale, as
// the next revision of the Form Content Document. The Form root lock is the
// domain fence, so the fence only names the source locale.
func advanceFormSourceLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	documentID string,
	expectedRevision uuid.UUID,
	sourceLocale string,
	apply func(context.Context, *gorm.DB) (bool, error),
) (contentblock.AdvanceResult, error) {
	document, err := formContentDocumentID(documentID)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	return store.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{DocumentID: document, ExpectedRevision: expectedRevision},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			applied, err := apply(ctx, tx)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{
				Changed: applied, AffectsTranslationSource: applied, ChangedLocales: []string{sourceLocale},
			}, nil
		},
	)
}

// formContentDocumentID parses a Form's Content Document reference.
func formContentDocumentID(value string) (uuid.UUID, error) {
	documentID, err := uuid.Parse(value)
	if err != nil || documentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("Form Content Document is not initialized").WithCause(failurecause.ServerFault(err))
	}
	return documentID, nil
}

func savedFormTargetRevision(
	ctx context.Context,
	tx *gorm.DB,
	save formDocumentSave,
	documentRevision string,
) (*string, error) {
	updated, updatedExists, err := loadFormAIDocumentLocale(ctx, tx, save.formID, save.locale, false)
	if err != nil {
		return nil, err
	}
	if !updatedExists {
		return nil, domainerr.Precondition("Form locale document disappeared")
	}
	targetRevision, err := deriveFormTargetRevision(documentRevision, updated)
	if err != nil {
		return nil, err
	}
	return &targetRevision, nil
}

func formDocumentRevisionChangedConflict(current string) error {
	return domainerr.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		"Form Content Document revision changed", domainerr.CollaborationRevisions{Document: current},
	)
}

func normalizeFormDocumentSaveError(err error) error {
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			targetConflict.Error(), domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	}
	return err
}

func canonicalFormCollaborationContributor(values []string) (string, error) {
	if len(values) != 1 {
		return "", domainerr.InvalidField("contributor_member_ids", "Form collaboration mutation requires exactly one origin Member")
	}
	value := strings.TrimSpace(values[0])
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return "", domainerr.InvalidField("contributor_member_ids", "Form collaboration mutation requires one canonical Member UUID")
	}
	return value, nil
}

type formCollaborativeSaveResult struct {
	locale           string
	documentRevision string
	targetRevision   *string
	changed          bool
	titleChanged     bool
}

// formCollaborativeLocaleUpdate is what a collaborative save changes in one
// form_translation row.
type formCollaborativeLocaleUpdate struct {
	titleChanged  bool
	title         *string
	schemaChanged bool
	schema        []byte
	updatedAt     time.Time
}

func formCollaborativeLocaleUpdates(
	current formAIDocumentLocaleRow,
	title *string,
	schema []byte,
	now time.Time,
) (formCollaborativeLocaleUpdate, bool) {
	update := formCollaborativeLocaleUpdate{
		titleChanged:  !formAIDocumentEqualString(current.Title, title),
		title:         title,
		schemaChanged: !formAIDocumentJSONEqual(current.Schema, schema),
		schema:        schema,
		updatedAt:     now,
	}
	return update, update.titleChanged
}

// columns is the form_translation column update, empty when nothing changed.
func (u formCollaborativeLocaleUpdate) columns() structured.Fields {
	columns := structured.Fields{}
	if u.titleChanged {
		columns["title"] = u.title
	}
	if u.schemaChanged {
		columns["content_json"] = u.schema
		columns["content_text"] = formCanonicalContentText(u.schema)
	}
	if len(columns) != 0 {
		columns["updated_at"] = u.updatedAt
	}
	return columns
}

func applyFormCollaborativeLocaleUpdates(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	locale string,
	update formCollaborativeLocaleUpdate,
	now time.Time,
) (bool, error) {
	columns := update.columns()
	if len(columns) == 0 {
		return false, nil
	}
	result := tx.WithContext(ctx).Table("form_translation").
		Where("entity_id = ?::uuid AND locale = ?", formID, locale).
		Updates(columns)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, domainerr.Precondition("Form locale document disappeared")
	}
	if err := tx.WithContext(ctx).Table("form").Where("id = ?::uuid", formID).
		Update("updated_at", now).Error; err != nil {
		return false, err
	}
	return true, nil
}

// LoadFormDocumentQuery reads one Form room's canonical locale state.
type LoadFormDocumentQuery struct {
	FormID string
	Locale string
}

// LoadFormDocument returns only canonical persisted Form values. Collab turns
// this response into a transient Y.Doc and cannot revive historical Yjs bytes.
func (s *Service) LoadFormDocument(ctx context.Context, query LoadFormDocumentQuery) (*intrav1.LoadFormDocumentResponse, error) {
	locale := strings.TrimSpace(query.Locale)
	if locale == "" {
		return nil, domainerr.Required("locale")
	}
	var response *intrav1.LoadFormDocumentResponse
	err := mutation.Read(ctx, s.db, func(tx *gorm.DB) error {
		root, err := loadFormAIDocumentRoot(ctx, tx, query.FormID, "")
		if err != nil {
			return err
		}
		source, sourceMeta, err := loadFormDocumentSource(ctx, tx, query.FormID, root.SourceLocale)
		if err != nil {
			return err
		}
		response = &intrav1.LoadFormDocumentResponse{
			SourceMetadata: sourceMeta, SourceLocale: root.SourceLocale, Locale: locale, DocumentRevision: root.DocumentRevision,
		}
		requested, requestedExists := source, true
		if locale != root.SourceLocale {
			if requested, requestedExists, err = loadFormAIDocumentLocale(ctx, tx, query.FormID, locale, false); err != nil {
				return err
			}
		}
		response.LocaleExists = requestedExists
		if !requestedExists {
			return nil
		}
		return projectFormDocumentLocale(response, root, source, requested)
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// loadFormDocumentSource reads the Form source locale document a room
// projects every locale over.
func loadFormDocumentSource(ctx context.Context, tx *gorm.DB, formID, sourceLocale string) (formAIDocumentLocaleRow, *intrav1.FormMeta, error) {
	source, exists, err := loadFormAIDocumentLocale(ctx, tx, formID, sourceLocale, false)
	if err != nil {
		return formAIDocumentLocaleRow{}, nil, err
	}
	if !exists {
		return formAIDocumentLocaleRow{}, nil, domainerr.Precondition("Form source locale document is missing")
	}
	meta, err := formMetaFromCanonicalRow(source)
	if err != nil {
		return formAIDocumentLocaleRow{}, nil, domainerr.Precondition("Form source locale document is invalid").WithCause(failurecause.ServerFault(err))
	}
	return source, meta, nil
}

// projectFormDocumentLocale sets the requested locale's metadata, present
// values and, for a target locale, its revision on response.
func projectFormDocumentLocale(response *intrav1.LoadFormDocumentResponse, root formAIDocumentRoot, source, requested formAIDocumentLocaleRow) error {
	invalid := func(err error) error {
		return domainerr.Precondition("Form requested locale document is invalid").WithCause(failurecause.ServerFault(err))
	}
	var err error
	if response.LocaleMetadata, err = formMetaFromCanonicalRow(requested); err != nil {
		return invalid(err)
	}
	isTarget := response.Locale != root.SourceLocale
	if isTarget {
		if err := validateFormAIDocumentTargetSchema(source.Schema, requested.Schema); err != nil {
			return invalid(err)
		}
	}
	if response.PresentLocaleValues, err = formCanonicalLocaleTargets(requested.Title, requested.Schema); err != nil {
		return invalid(err)
	}
	if !isTarget {
		return nil
	}
	revision, err := deriveFormTargetRevision(root.DocumentRevision, requested)
	if err != nil {
		return err
	}
	response.TargetRevision = &revision
	return nil
}

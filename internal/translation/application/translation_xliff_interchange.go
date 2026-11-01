package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const translationXLIFFMaximumBytes = 20 << 20

// TranslationXLIFFArtifact is the bounded server-side payload handed to the
// existing File ingest/storage authority. It never crosses MCP or Connect as
// XML/base64.
type TranslationXLIFFArtifact struct {
	Body     []byte
	FileName string
	MimeType string
}

// VerifiedTranslationXLIFF is one completed File upload read through the File
// authority. Implementations must verify object identity, completed ingest,
// stored size, and MIME before returning Body.
type VerifiedTranslationXLIFF struct {
	FileID    string
	Extension string
	MimeType  string
	Body      []byte
}

// TranslationXLIFFFiles is the consumer-owned boundary to existing File
// ingest/storage. Export creates a short-lived File delivery reference; import
// reads an already verified upload and never accepts caller-supplied XML.
type TranslationXLIFFFiles interface {
	CreateTranslationXLIFF(context.Context, TranslationXLIFFArtifact) (*commonv1.ExpiringMediaRef, error)
	ReadVerifiedTranslationXLIFF(context.Context, string, int64) (VerifiedTranslationXLIFF, error)
}

// TranslationInterchangeTargetState is the domain adapter's projection of the
// current locale-owned values. Revision is derived from authoritative current
// owning-domain facts; it is not freshness, history, or source identity.
type TranslationInterchangeTargetState struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

// TranslationInterchangeApply is a generic validated target replacement or
// patch. The owning-domain adapter must lock/rederive ExpectedRevision, apply
// the locale mutation and Audit, and return the new derived revision in one
// transaction.
type TranslationInterchangeApply struct {
	EntityType       string
	EntityID         string
	SourceLocale     string
	TargetLocale     string
	Mode             managev1.TranslationInterchangeMode
	ExpectedRevision *string
	Source           *translation.SourceDocument
	Plan             *translation.ExtractionPlan
	Targets          map[string]translation.UnitResult
	UnitHandles      []string
	Now              time.Time
}

type TranslationInterchangeApplyResult struct {
	Revision            string
	Changed             bool
	AffectedUnitHandles []string
}

// TranslationInterchangeDomains owns target-row/document locking, CAS,
// locale-value projection, mutation, Audit and collaboration relay for all 15
// translation domains. Translation application owns XLIFF and request mapping,
// not domain persistence.
type TranslationInterchangeDomains interface {
	LoadTranslationInterchangeTarget(
		context.Context,
		*gorm.DB,
		*contentblock.Store,
		string,
		string,
		string,
		*translation.ExtractionPlan,
	) (TranslationInterchangeTargetState, error)
	ApplyTranslationInterchange(
		context.Context,
		*gorm.DB,
		*contentblock.Store,
		TranslationInterchangeApply,
	) (TranslationInterchangeApplyResult, error)
}

// ExportTranslationXLIFFCommand exports one target locale of an entity as an
// XLIFF 2.2 File: every current unit for REPLACE, the selected UnitHandles
// for PATCH.
type ExportTranslationXLIFFCommand struct {
	Target       *managev1.TranslationTarget
	TargetLocale string
	Mode         managev1.TranslationInterchangeMode
	UnitHandles  []string
}

// ExportEntityTranslationXLIFF builds the XLIFF under the entity's view
// authority and stores it as a short-lived File.
func (s *Service) ExportEntityTranslationXLIFF(ctx context.Context, command ExportTranslationXLIFFCommand) (*managev1.ExportEntityTranslationXLIFFResponse, error) {
	entityType, entityID, targetLocale, err := s.validateTranslationInterchangeRequest(
		ctx, command.Target, command.TargetLocale, command.Mode,
	)
	if err != nil {
		return nil, err
	}
	unitHandles, err := validateXLIFFExportSelection(command.Mode, command.UnitHandles)
	if err != nil {
		return nil, err
	}

	var artifact TranslationXLIFFArtifact
	var sourceLocale string
	var targetRevision *string
	// The view check locks the root and the principal; the build reads
	// their committed state.
	err = mutation.ReadCurrent(ctx, s.db, func(tx *gorm.DB) error {
		if err := s.requireTranslationInterchangeView(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		plan, _, planErr := s.buildTranslationInterchangePlan(ctx, tx, entityType, entityID, targetLocale)
		if planErr != nil {
			return planErr
		}
		state, stateErr := s.interchangeDomains.LoadTranslationInterchangeTarget(
			ctx, tx, s.contentBlocks, entityType, entityID, targetLocale, plan,
		)
		if stateErr != nil {
			return stateErr
		}
		if stateErr = validateTranslationInterchangeTargetState(state, plan); stateErr != nil {
			return stateErr
		}

		document, buildErr := translation.BuildXLIFFDocument(plan)
		if buildErr != nil {
			return mapTranslationInterchangeBuildError(buildErr)
		}
		withTargets := translation.XLIFFWithTargets(*document, state.Targets)
		selected, selectErr := selectXLIFFUnits(withTargets, command.Mode, unitHandles)
		if selectErr != nil {
			return selectErr
		}
		body, marshalErr := translation.MarshalXLIFF(&selected)
		if marshalErr != nil {
			return marshalErr
		}
		artifact = TranslationXLIFFArtifact{
			Body: body, MimeType: "application/xliff+xml",
			FileName: fmt.Sprintf("%s-%s-%s.xlf", entityType, entityID, targetLocale),
		}
		sourceLocale = plan.SourceLocale
		if state.Exists {
			revision := state.Revision
			targetRevision = &revision
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ref, err := s.xliffFiles.CreateTranslationXLIFF(ctx, artifact)
	if err != nil {
		return nil, err
	}
	if err := validateTranslationXLIFFArtifactRef(ref, s.now().UTC()); err != nil {
		return nil, err
	}
	return &managev1.ExportEntityTranslationXLIFFResponse{
		Artifact: ref, SourceLocale: sourceLocale, TargetLocale: targetLocale,
		TargetRevision: targetRevision, Mode: command.Mode,
	}, nil
}

// ImportTranslationXLIFFCommand imports the XLIFF targets of an uploaded File
// into one target locale. ExpectedTargetRevision, when set, must match the
// target's current revision.
type ImportTranslationXLIFFCommand struct {
	Target                 *managev1.TranslationTarget
	TargetLocale           string
	Mode                   managev1.TranslationInterchangeMode
	FileID                 string
	ExpectedTargetRevision *string
}

// ImportEntityTranslationXLIFF validates the upload against the current
// extraction plan and applies its targets through the owning domain.
func (s *Service) ImportEntityTranslationXLIFF(ctx context.Context, command ImportTranslationXLIFFCommand) (*managev1.ImportEntityTranslationXLIFFResponse, error) {
	entityType, entityID, targetLocale, err := s.validateTranslationInterchangeRequest(
		ctx, command.Target, command.TargetLocale, command.Mode,
	)
	if err != nil {
		return nil, err
	}
	fileID, err := canonicalTranslationXLIFFFileID(command.FileID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := validateExpectedTranslationTargetRevision(command.ExpectedTargetRevision)
	if err != nil {
		return nil, err
	}

	upload, err := s.xliffFiles.ReadVerifiedTranslationXLIFF(ctx, fileID, translationXLIFFMaximumBytes)
	if err != nil {
		return nil, err
	}
	if err := validateVerifiedTranslationXLIFF(upload, fileID); err != nil {
		return nil, err
	}
	imported, err := translation.UnmarshalXLIFFInterchange(upload.Body)
	if err != nil {
		return nil, domainerr.InvalidField("file_id", err.Error())
	}
	if imported.File.ID != entityType+":"+entityID || imported.TargetLocale != targetLocale {
		return nil, domainerr.InvalidField("file_id", "XLIFF target identity does not match the request")
	}

	now := s.now().UTC()
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[TranslationInterchangeApplyResult]{
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return s.requireTranslationInterchangeEdit(ctx, tx, entityType, entityID)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (TranslationInterchangeApplyResult, error) {
			return s.applyImportedXLIFF(ctx, tx, *imported, TranslationInterchangeApply{
				EntityType: entityType, EntityID: entityID, TargetLocale: targetLocale,
				Mode: command.Mode, ExpectedRevision: expectedRevision, Now: now,
			})
		},
		// The owning domain appends the locale-content record in its apply.
		Audit: func(TranslationInterchangeApplyResult) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return nil, err
	}
	return &managev1.ImportEntityTranslationXLIFFResponse{
		TargetRevision: result.Revision, Changed: result.Changed,
		AffectedUnitHandles: append([]string(nil), result.AffectedUnitHandles...),
	}, nil
}

// applyImportedXLIFF applies the imported targets that still match the
// current extraction plan and requests the target locale's OG.
func (s *Service) applyImportedXLIFF(
	ctx context.Context,
	tx *gorm.DB,
	imported translation.XLIFFDocument,
	apply TranslationInterchangeApply,
) (TranslationInterchangeApplyResult, error) {
	plan, source, err := s.buildTranslationInterchangePlan(ctx, tx, apply.EntityType, apply.EntityID, apply.TargetLocale)
	if err != nil {
		return TranslationInterchangeApplyResult{}, err
	}
	if imported.SourceLocale != plan.SourceLocale {
		return TranslationInterchangeApplyResult{}, domainerr.InvalidField("file_id", "XLIFF source locale no longer matches the owning document")
	}
	targets, handles, err := validateImportedXLIFFAgainstCurrentPlan(imported, plan, apply.Mode)
	if err != nil {
		if invalid, ok := errors.AsType[*domainerr.Error](err); ok && invalid.Kind == domainerr.Invalid {
			return TranslationInterchangeApplyResult{}, err
		}
		return TranslationInterchangeApplyResult{}, domainerr.InvalidField("file_id", err.Error())
	}
	apply.SourceLocale, apply.Source, apply.Plan = plan.SourceLocale, source, plan
	apply.Targets, apply.UnitHandles = targets, handles
	result, err := s.interchangeDomains.ApplyTranslationInterchange(ctx, tx, s.contentBlocks, apply)
	if err != nil {
		return TranslationInterchangeApplyResult{}, err
	}
	if result.Revision == "" {
		return TranslationInterchangeApplyResult{}, errors.New("translation interchange apply returned no target revision")
	}
	if result.Changed {
		if _, err := s.domains.RequestLocaleOG(
			ctx, tx, s.ogPlanner, s.ogRefresher,
			apply.EntityType, apply.EntityID, apply.TargetLocale, "translation_xliff_import",
		); err != nil {
			return TranslationInterchangeApplyResult{}, err
		}
	}
	return result, nil
}

func (s *Service) requireTranslationInterchangeView(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) error {
	return s.domains.RequireTranslationInterchangeView(
		ctx, tx, s.spiceDB, entityType, entityID,
	)
}

func (s *Service) requireTranslationInterchangeEdit(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) error {
	return s.domains.RequireTranslationInterchangeEdit(
		ctx, tx, s.spiceDB, entityType, entityID,
	)
}

func (s *Service) validateTranslationInterchangeRequest(
	ctx context.Context,
	target *managev1.TranslationTarget,
	targetLocale string,
	mode managev1.TranslationInterchangeMode,
) (string, string, string, error) {
	entityType, entityID, err := parseTranslationTarget(target)
	if err != nil {
		return "", "", "", err
	}
	if mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return "", "", "", domainerr.InvalidField("mode", "PATCH or REPLACE is required")
	}
	targetLocale = strings.TrimSpace(targetLocale)
	if targetLocale == "" {
		return "", "", "", domainerr.InvalidField("target_locale", "target locale is required")
	}
	if _, err := localization.NewCatalog(s.db).Find(ctx, targetLocale); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", domainerr.InvalidField("target_locale", "unsupported locale")
		}
		return "", "", "", err
	}
	return entityType, entityID, targetLocale, nil
}

func (s *Service) buildTranslationInterchangePlan(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	targetLocale string,
) (*translation.ExtractionPlan, *translation.SourceDocument, error) {
	authority, err := loadTranslationDocumentAuthority(ctx, db, entityType, entityID)
	if err != nil {
		return nil, nil, err
	}
	if authority.SourceLocale == targetLocale {
		return nil, nil, domainerr.InvalidField("target_locale", "target locale must differ from source locale")
	}
	source, err := s.domains.LoadSourceDocument(ctx, db, s.contentBlocks, entityType, entityID)
	if err != nil {
		return nil, nil, err
	}
	source.SourceLocale = authority.SourceLocale
	source.ContentDocumentRevision = authority.DocumentRevision.String()
	plan, err := s.domains.BuildExtractionPlan(&model.TranslationJob{
		EntityType: entityType, EntityID: entityID,
		SourceLocale: authority.SourceLocale, TargetLocale: targetLocale,
	}, source)
	if err != nil {
		return nil, nil, mapTranslationInterchangeBuildError(err)
	}
	return plan, source, nil
}

func mapTranslationInterchangeBuildError(err error) error {
	if errors.Is(err, translation.ErrNoTranslatableUnits) {
		return domainerr.Precondition("no translatable content")
	}
	return err
}

func validateXLIFFExportSelection(mode managev1.TranslationInterchangeMode, values []string) ([]string, error) {
	handles := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		handle := strings.TrimSpace(value)
		if handle == "" {
			return nil, domainerr.InvalidField("unit_handles", "unit handles must be non-empty")
		}
		if _, duplicate := seen[handle]; duplicate {
			return nil, domainerr.InvalidField("unit_handles", "unit handles must be unique")
		}
		seen[handle] = struct{}{}
		handles = append(handles, handle)
	}
	switch mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		if len(handles) == 0 {
			return nil, domainerr.InvalidField("unit_handles", "PATCH requires a non-empty explicit unit selection")
		}
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		if len(handles) != 0 {
			return nil, domainerr.InvalidField("unit_handles", "REPLACE requires the complete manifest and no unit selection")
		}
	}
	return handles, nil
}

func selectXLIFFUnits(
	document translation.XLIFFDocument,
	mode managev1.TranslationInterchangeMode,
	handles []string,
) (translation.XLIFFDocument, error) {
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return document, nil
	}
	requested := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		requested[handle] = struct{}{}
	}
	selected := document
	selected.File.Groups = make([]translation.XLIFFGroup, 0, len(document.File.Groups))
	for _, group := range document.File.Groups {
		next := group
		next.TranslationUnit = make([]translation.XLIFFUnit, 0, len(group.TranslationUnit))
		for _, unit := range group.TranslationUnit {
			if _, ok := requested[unit.ID]; ok {
				next.TranslationUnit = append(next.TranslationUnit, unit)
				delete(requested, unit.ID)
			}
		}
		if len(next.TranslationUnit) != 0 {
			selected.File.Groups = append(selected.File.Groups, next)
		}
	}
	if len(requested) != 0 {
		unknown := make([]string, 0, len(requested))
		for handle := range requested {
			unknown = append(unknown, handle)
		}
		slices.Sort(unknown)
		return translation.XLIFFDocument{}, domainerr.InvalidField(
			"unit_handles", "unknown unit handles: "+strings.Join(unknown, ", "),
		)
	}
	return selected, nil
}

func validateImportedXLIFFAgainstCurrentPlan(
	imported translation.XLIFFDocument,
	plan *translation.ExtractionPlan,
	mode managev1.TranslationInterchangeMode,
) (map[string]translation.UnitResult, []string, error) {
	current, err := translation.BuildXLIFFDocument(plan)
	if err != nil {
		return nil, nil, err
	}
	importedTargets := translation.XLIFFTargets(imported)
	importedHandles := make(map[string]struct{}, len(importedTargets))
	for _, group := range imported.File.Groups {
		for _, unit := range group.TranslationUnit {
			importedHandles[unit.ID] = struct{}{}
		}
	}
	if err := validateImportedXLIFFManifest(importedHandles, plan, mode); err != nil {
		return nil, nil, err
	}

	results := make(map[string]translation.UnitResult)
	handles := make([]string, 0, len(importedTargets))
	validation := *current
	validation.File.Groups = make([]translation.XLIFFGroup, 0, len(current.File.Groups))
	for _, group := range current.File.Groups {
		next := group
		next.TranslationUnit = make([]translation.XLIFFUnit, 0, len(group.TranslationUnit))
		for _, unit := range group.TranslationUnit {
			if _, exported := importedHandles[unit.ID]; !exported {
				continue
			}
			result, translated := importedTargets[unit.ID]
			if !translated {
				return nil, nil, fmt.Errorf("XLIFF unit %q target is required", unit.ID)
			}
			result.OriginalData = append([]translation.XLIFFOriginalData(nil), unit.OriginalData...)
			results[unit.ID] = result
			handles = append(handles, unit.ID)
			target := result.TranslatedText
			unit.Target = &target
			unit.TargetInline = append([]translation.XLIFFInline(nil), result.TargetInline...)
			next.TranslationUnit = append(next.TranslationUnit, unit)
		}
		if len(next.TranslationUnit) != 0 {
			validation.File.Groups = append(validation.File.Groups, next)
		}
	}
	if len(validation.File.Groups) != 0 {
		if err := translation.ValidateXLIFFInterchangeDocument(&validation); err != nil {
			return nil, nil, err
		}
	}
	return results, handles, nil
}

// validateImportedXLIFFManifest checks the imported unit handles against the
// current plan: every handle must be current, PATCH names at least one and
// REPLACE names them all.
func validateImportedXLIFFManifest(
	importedHandles map[string]struct{},
	plan *translation.ExtractionPlan,
	mode managev1.TranslationInterchangeMode,
) error {
	currentHandles := make(map[string]struct{}, len(plan.Units))
	for _, unit := range plan.Units {
		currentHandles[unit.UnitID] = struct{}{}
	}
	for handle := range importedHandles {
		if _, exists := currentHandles[handle]; !exists {
			return domainerr.InvalidField(
				"file_id", fmt.Sprintf("XLIFF contains unknown current stable unit %q", handle),
			)
		}
	}
	switch mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		if len(importedHandles) == 0 {
			return domainerr.InvalidField("file_id", "PATCH requires a non-empty explicit unit manifest")
		}
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		if len(importedHandles) != len(currentHandles) {
			return domainerr.InvalidField("file_id", "REPLACE requires the complete current stable unit manifest")
		}
		for handle := range currentHandles {
			if _, exists := importedHandles[handle]; !exists {
				return domainerr.InvalidField("file_id", fmt.Sprintf("REPLACE is missing current stable unit %q", handle))
			}
		}
	default:
		return domainerr.InvalidField("mode", "PATCH or REPLACE is required")
	}
	return nil
}

func validateTranslationInterchangeTargetState(
	state TranslationInterchangeTargetState,
	plan *translation.ExtractionPlan,
) error {
	if !state.Exists {
		if state.Revision != "" || len(state.Targets) != 0 {
			return fmt.Errorf("absent target returned revision or values")
		}
		return nil
	}
	if strings.TrimSpace(state.Revision) == "" {
		return fmt.Errorf("present target returned no revision")
	}
	known := make(map[string]struct{}, len(plan.Units))
	for _, unit := range plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	for handle := range state.Targets {
		if _, ok := known[handle]; !ok {
			return fmt.Errorf("target returned unknown unit %q", handle)
		}
	}
	return nil
}

func canonicalTranslationXLIFFFileID(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return "", domainerr.InvalidField("file_id", "a canonical File UUID is required")
	}
	return value, nil
}

func validateExpectedTranslationTargetRevision(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" || normalized != *value || len(normalized) > 256 {
		return nil, domainerr.InvalidField("expected_target_revision", "an exact opaque target revision is required")
	}
	return &normalized, nil
}

func validateVerifiedTranslationXLIFF(upload VerifiedTranslationXLIFF, expectedFileID string) error {
	if upload.FileID != expectedFileID {
		return domainerr.InvalidField("file_id", "verified File identity does not match the request")
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(upload.MimeType, ";")[0]))
	if mimeType != "application/xliff+xml" && mimeType != "application/xml" && mimeType != "text/xml" {
		return domainerr.InvalidField("file_id", "File MIME type is not XLIFF/XML")
	}
	extension := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(upload.Extension)), ".")
	// Existing uploads can be stored as bin/xml because File's MIME catalog is
	// intentionally format-agnostic. MIME and the strict XLIFF parser remain
	// authoritative; requiring only xlf/xliff would reject valid File handles.
	if extension != "xlf" && extension != "xliff" && extension != "xml" && extension != "bin" {
		return domainerr.InvalidField("file_id", "File must be an XLIFF artifact")
	}
	if len(upload.Body) == 0 || len(upload.Body) > translationXLIFFMaximumBytes {
		return domainerr.InvalidField("file_id", "XLIFF File is empty or exceeds the size limit")
	}
	return nil
}

func validateTranslationXLIFFArtifactRef(ref *commonv1.ExpiringMediaRef, now time.Time) error {
	if ref == nil {
		return fmt.Errorf("translation XLIFF artifact reference is required")
	}
	if _, err := canonicalTranslationXLIFFFileID(ref.FileId); err != nil {
		return fmt.Errorf("translation XLIFF artifact returned an invalid File ID: %w", err)
	}
	parsed, err := url.Parse(ref.Url)
	if err != nil {
		return failurecause.WithAttrs(
			fmt.Errorf("translation XLIFF artifact returned an invalid delivery URL: %w", err),
			slog.String("file_id", ref.FileId),
		)
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("translation XLIFF artifact returned an invalid delivery URL")
	}
	if ref.ExpiresAt == nil || !ref.ExpiresAt.IsValid() || !ref.ExpiresAt.AsTime().After(now) {
		return fmt.Errorf("translation XLIFF artifact must be short-lived")
	}
	if ref.Purpose != commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD {
		return fmt.Errorf("translation XLIFF artifact must use download delivery")
	}
	return nil
}

package work

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type ProjectRichTextInterchangeTargets func(
	*translation.ExtractionPlan,
	*contentv1.LocalizedRichTextDocument,
) (map[string]translation.UnitResult, error)

type BuildRichTextInterchangePatch func(
	*translation.ExtractionPlan,
	*contentv1.LocalizedRichTextDocument,
	*contentv1.LocalizedRichTextDocument,
	map[string]translation.UnitResult,
) (*contentv1.RichTextLocaleOverlay, error)

// TranslationInterchangeTarget is the Work-owned projection used by XLIFF
// export. Row existence remains the target-presence authority.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

// TranslationInterchangeMutation is one already validated XLIFF mutation.
// The Work domain still rechecks identity, source role, target CAS and current
// target facts under its aggregate locks before writing.
type TranslationInterchangeMutation struct {
	WorkID           string
	SourceLocale     string
	TargetLocale     string
	Mode             managev1.TranslationInterchangeMode
	ExpectedRevision *string
	Source           *translation.SourceDocument
	Plan             *translation.ExtractionPlan
	Targets          map[string]translation.UnitResult
	UnitHandles      []string
	Now              time.Time
	ProjectTargets   ProjectRichTextInterchangeTargets
	BuildPatch       BuildRichTextInterchangePatch
}

// TranslationInterchangeResult contains only the accepted locale revision and
// the stable unit handles whose values changed.
type TranslationInterchangeResult struct {
	Revision               string
	Changed                bool
	AffectedUnitHandles    []string
	TargetPreviouslyExists bool
}

type workInterchangeState struct {
	documentID     string
	snapshot       contentblock.Snapshot
	metadata       workAIDocumentLocale
	document       *contentv1.LocalizedRichTextDocument
	exists         bool
	targets        map[string]translation.UnitResult
	targetRevision string
}

// LoadTranslationInterchangeTarget loads a coherent Work target after the
// caller has completed request authorization and locked the owning root.
func LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	workID string,
	locale string,
	plan *translation.ExtractionPlan,
	project ProjectRichTextInterchangeTargets,
) (TranslationInterchangeTarget, error) {
	state, err := loadWorkInterchangeState(ctx, tx, store, workID, locale, plan, "SHARE", project)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	return TranslationInterchangeTarget{
		Exists: state.exists, Revision: state.targetRevision, Targets: state.targets,
	}, nil
}

// ApplyTranslationInterchange applies PATCH to only selected Blocks and
// metadata fields. REPLACE uses the validated complete/current intersection
// supplied by Translation application. Both paths use Work's existing
// metadata presence and Content Block mutation boundary.
func ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if err := validateWorkInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	state, err := loadWorkInterchangeState(
		ctx, tx, store, mutation.WorkID, mutation.TargetLocale, mutation.Plan, "UPDATE", mutation.ProjectTargets,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if err := translation.ValidateExpectedTargetRevision(
		mutation.ExpectedRevision, state.targetRevision, state.exists,
	); err != nil {
		var conflict *translation.TargetRevisionConflict
		if errors.As(err, &conflict) {
			return TranslationInterchangeResult{}, errs.FailedPrecondition(err.Error())
		}
		return TranslationInterchangeResult{}, errs.Internal(err)
	}

	desired := make(map[string]translation.UnitResult, len(state.targets)+len(mutation.Targets))
	if mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
		for handle, target := range state.targets {
			desired[handle] = target
		}
	}
	for handle, target := range mutation.Targets {
		desired[handle] = target
	}
	targetDocument := state.document
	if mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		emptyTarget := proto.Clone(state.document).(*contentv1.LocalizedRichTextDocument)
		emptyTarget.LocaleOverlay = &contentv1.RichTextLocaleOverlay{Locale: mutation.TargetLocale}
		targetDocument = emptyTarget
	}
	patch, err := mutation.BuildPatch(
		mutation.Plan, mutation.Source.ContentBlockDocument, targetDocument, mutation.Targets,
	)
	if err != nil {
		return TranslationInterchangeResult{}, errs.InvalidArgument("file_id", err.Error())
	}
	batch := contentblock.Batch{
		DocumentID: state.snapshot.Document.ID, ExpectedRevision: state.snapshot.Document.Revision,
	}
	blockMutations := workInterchangeBlockMutations(
		state.document.GetLocaleOverlay(), patch,
		mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
	)
	if len(blockMutations) != 0 {
		batch, err = contentblock.BatchFromRichTextSystemProto(
			state.snapshot.Document.ID,
			&contentv1.RichTextBlockMutationBatch{
				BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
				Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
				ExpectedRevision:        state.snapshot.Document.Revision.String(),
				LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
					Locale:    mutation.TargetLocale,
					Mutations: blockMutations,
				}},
			},
		)
		if err != nil {
			return TranslationInterchangeResult{}, normalizeWorkContentBlockError(err)
		}
	}

	metadataPatch := workInterchangeMetadataPatch(mutation.Plan, desired)
	result, targetRevision, err := applyWorkTargetLocaleBatch(
		ctx, tx, store, mutation.WorkID, state.snapshot.Document.ID,
		mutation.TargetLocale, batch, mutation.ExpectedRevision,
		workTargetMetadataPatch{
			EnsureLocale:  metadataPatch.EnsureLocale,
			UpdateTitle:   metadataPatch.SetTitle,
			Title:         cloneOptionalString(metadataPatch.Title),
			UpdateSummary: metadataPatch.SetSummary,
			Summary:       cloneOptionalString(metadataPatch.Summary),
		},
		true, false,
		mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		false, mutation.Now.UTC(), workSystemTranslationDocumentFence(mutation.WorkID),
	)
	if err != nil {
		return TranslationInterchangeResult{}, normalizeWorkContentBlockError(err)
	}
	if result.TranslationSourceChanged {
		return TranslationInterchangeResult{}, errs.InternalMsg("target Work interchange changed the source-owned Block view")
	}
	if targetRevision == nil {
		return TranslationInterchangeResult{}, errs.InternalMsg("Work interchange apply did not create its target locale")
	}
	affected := changedWorkInterchangeHandles(state.targets, mutation.Targets, mutation.UnitHandles)
	if !result.Changed {
		affected = nil
	}
	return TranslationInterchangeResult{
		Revision: *targetRevision, Changed: result.Changed, AffectedUnitHandles: affected,
		TargetPreviouslyExists: state.exists,
	}, nil
}

func validateWorkInterchangeMutation(mutation TranslationInterchangeMutation) error {
	if mutation.WorkID == "" || mutation.Source == nil || mutation.Plan == nil || mutation.Targets == nil ||
		mutation.ProjectTargets == nil || mutation.BuildPatch == nil {
		return errs.InternalMsg("Work translation interchange mutation is incomplete")
	}
	if mutation.Plan.EntityType != "work" || mutation.Plan.EntityID != mutation.WorkID ||
		mutation.Plan.SourceLocale != mutation.SourceLocale ||
		mutation.Plan.TargetLocale != mutation.TargetLocale ||
		mutation.Source.ContentBlockDocument == nil ||
		mutation.Source.ContentBlockDocument.GetLocale() != mutation.SourceLocale {
		return errs.InvalidArgument("file_id", "Work XLIFF identity does not match the current document")
	}
	if mutation.SourceLocale == mutation.TargetLocale {
		return errs.InvalidArgument("target_locale", "Work source translation cannot be imported as a target")
	}
	if mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return errs.InvalidArgument("file_id", "Work XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return errs.InvalidArgument("file_id", "Work XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return errs.InvalidArgument("file_id", "Work XLIFF contains an unknown stable unit")
		}
		if target, ok := mutation.Targets[handle]; !ok || target.UnitID != handle {
			return errs.InvalidArgument("file_id", "Work XLIFF target identity does not match its stable unit")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return errs.InvalidArgument("file_id", "Work XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func loadWorkInterchangeState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	workID string,
	locale string,
	plan *translation.ExtractionPlan,
	lock string,
	project ProjectRichTextInterchangeTargets,
) (workInterchangeState, error) {
	if tx == nil || store == nil || plan == nil || plan.EntityType != "work" ||
		plan.EntityID != workID || plan.TargetLocale != locale || project == nil {
		return workInterchangeState{}, errs.InternalMsg("Work translation interchange load identity is invalid")
	}
	root, documentID, err := loadWorkAIDocumentRoot(ctx, tx, workID, lock)
	if err != nil {
		return workInterchangeState{}, err
	}
	_ = root
	domain, err := loadWorkContentDomainContext(ctx, tx, workID)
	if err != nil {
		return workInterchangeState{}, err
	}
	if domain.SourceLocale != plan.SourceLocale || locale == domain.SourceLocale {
		return workInterchangeState{}, errs.InvalidArgument("target_locale", "Work XLIFF locale role no longer matches the owning document")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return workInterchangeState{}, normalizeWorkContentBlockError(err)
	}
	metadata, exists, err := loadWorkAIDocumentLocaleForUpdate(ctx, tx, workID, locale)
	if err != nil {
		return workInterchangeState{}, err
	}
	state := workInterchangeState{
		documentID: documentID.String(), snapshot: snapshot, metadata: metadata, exists: exists,
		targets: make(map[string]translation.UnitResult),
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return workInterchangeState{}, normalizeWorkContentBlockError(err)
	}
	state.document = document
	if !exists {
		return state, nil
	}
	state.targets, err = projectWorkInterchangeTargets(plan, metadata, document, project)
	if err != nil {
		return workInterchangeState{}, err
	}
	state.targetRevision, err = workInterchangeTargetRevision(
		ctx, tx, workID, locale, snapshot.Document.Revision.String(), true,
	)
	if err != nil {
		return workInterchangeState{}, err
	}
	return state, nil
}

func projectWorkInterchangeTargets(
	plan *translation.ExtractionPlan,
	metadata workAIDocumentLocale,
	document *contentv1.LocalizedRichTextDocument,
	project ProjectRichTextInterchangeTargets,
) (map[string]translation.UnitResult, error) {
	targets, err := project(plan, document)
	if err != nil {
		return nil, err
	}
	if metadata.Title != nil {
		targets["entity:title"] = translation.UnitResult{UnitID: "entity:title", TranslatedText: *metadata.Title}
	}
	if metadata.Summary != nil {
		targets["entity:summary"] = translation.UnitResult{UnitID: "entity:summary", TranslatedText: *metadata.Summary}
	}
	return targets, nil
}

func workInterchangeBlockMutations(
	current *contentv1.RichTextLocaleOverlay,
	replacement *contentv1.RichTextLocaleOverlay,
	replace bool,
) []*contentv1.RichTextBlockLocaleMutation {
	blocks := replacement.GetBlocks()
	mutations := make([]*contentv1.RichTextBlockLocaleMutation, 0, len(blocks)+len(current.GetBlocks()))
	replaced := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		replaced[block.GetBlockId()] = struct{}{}
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block},
			},
		})
	}
	if !replace {
		return mutations
	}
	deleted := make([]string, 0)
	for _, block := range current.GetBlocks() {
		if _, exists := replaced[block.GetBlockId()]; !exists {
			deleted = append(deleted, block.GetBlockId())
		}
	}
	sort.Strings(deleted)
	for _, blockID := range deleted {
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Delete{
				Delete: &contentv1.DeleteRichTextBlockLocale{BlockId: blockID},
			},
		})
	}
	return mutations
}

func workInterchangeMetadataPatch(plan *translation.ExtractionPlan, desired map[string]translation.UnitResult) AIDocumentMetadataPatch {
	patch := AIDocumentMetadataPatch{EnsureLocale: true}
	for _, unit := range plan.Units {
		switch unit.UnitID {
		case "entity:title":
			patch.SetTitle = true
			if target, ok := desired[unit.UnitID]; ok {
				patch.Title = stringPointer(target.TranslatedText)
			}
		case "entity:summary":
			patch.SetSummary = true
			if target, ok := desired[unit.UnitID]; ok {
				patch.Summary = stringPointer(target.TranslatedText)
			}
		}
	}
	return patch
}

func workInterchangeTargetRevision(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	locale string,
	documentRevision string,
	exists bool,
) (string, error) {
	if !exists {
		return translation.DeriveTargetRevision(translation.TargetRevisionFacts{})
	}
	var row struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	result := tx.WithContext(ctx).Table("work_translation").Select("updated_at").
		Where("entity_id = ? AND locale = ?", workID, locale).Take(&row)
	if result.Error != nil {
		return "", errs.Internal(result.Error)
	}
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &row.UpdatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func changedWorkInterchangeHandles(
	current map[string]translation.UnitResult,
	incoming map[string]translation.UnitResult,
	handles []string,
) []string {
	affected := make([]string, 0, len(handles))
	for _, handle := range handles {
		if !reflect.DeepEqual(current[handle], incoming[handle]) {
			affected = append(affected, handle)
		}
	}
	sort.Strings(affected)
	return affected
}

func stringPointer(value string) *string { return &value }

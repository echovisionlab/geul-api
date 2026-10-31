package menu

import (
	"context"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// MenuCollaborationCheckpointFence authorizes the contributors of a
// collaboration save inside its transaction.
type MenuCollaborationCheckpointFence interface {
	RequireCurrentContributorsForPermission(
		context.Context,
		*gorm.DB,
		intrav1.CollaborationResourceType,
		string,
		[]string,
		intrav1.CollaborationPermission,
	) error
}

// LoadMenuDocumentQuery reads one Menu source or existing target locale room.
// The collaboration document is only a transient projection of the Menu.
type LoadMenuDocumentQuery struct {
	MenuID string
	Locale string
}

// SaveMenuDocumentCommand saves one collaboration room. Items carry the
// collaboration item tree; ContributorMemberIDs name the one origin Member
// editor-collab derived from admitted frames.
type SaveMenuDocumentCommand struct {
	MenuID                   string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	Name                     string
	Items                    []*intrav1.MenuCollaborationItem
	RequestedLabels          map[string]string
	ContributorMemberIDs     []string
}

func (s *Service) LoadMenuDocument(ctx context.Context, query LoadMenuDocumentQuery) (*intrav1.LoadMenuDocumentResponse, error) {
	locale, err := canonicalMenuRoomLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	if err := requireCanonicalMenuCollaborationUUID(query.MenuID, "menu_id"); err != nil {
		return nil, err
	}
	snapshot, err := loadMenuAIDocumentSnapshot(ctx, s.db, query.MenuID, locale, false)
	if err != nil {
		return nil, err
	}
	return &intrav1.LoadMenuDocumentResponse{
		SourceLocale:     snapshot.SourceLocale,
		Locale:           snapshot.Locale,
		LocaleExists:     snapshot.LocaleExists,
		Name:             snapshot.Name,
		Items:            menuCollaborationItems(snapshot.Items),
		SourceLabels:     cloneMenuLabels(snapshot.sourceLabels),
		RequestedLabels:  cloneMenuLabels(snapshot.Labels),
		DocumentRevision: snapshot.DocumentRevision,
		TargetRevision:   optional.Clone(snapshot.TargetRevision),
	}, nil
}

// menuRoomSave is a validated collaboration save against the locked Menu.
type menuRoomSave struct {
	previous AIDocumentSnapshot
	compiled compiledMenuAIDocument
}

func (s *Service) SaveMenuDocument(ctx context.Context, command SaveMenuDocumentCommand) (*intrav1.SaveMenuDocumentResponse, error) {
	locale, err := canonicalMenuRoomLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	if err := requireCanonicalMenuCollaborationUUID(command.MenuID, "menu_id"); err != nil {
		return nil, err
	}
	if err := requireCanonicalMenuCollaborationUUID(command.ExpectedDocumentRevision, "expected_document_revision"); err != nil {
		return nil, err
	}
	contributor, err := canonicalMenuCollaborationContributor(command.ContributorMemberIDs)
	if err != nil {
		return nil, err
	}
	nextItems := menuAIDocumentItems(command.Items)
	targetReferences := collectMenuTargetReferences(modelItemsToProto(aiDocumentItemsToModel(nextItems)))

	var save menuRoomSave
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[AIDocumentApplyResult]{
		// editor-collab calls as itself: the save is authorized by the
		// contributors' current permission, which the checkpoint fence reads
		// once the room is locked and the change is known.
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			// Keep the domain-wide lock order used by direct Menu writes:
			// concrete targets first, then the owning Menu root and locale rows.
			if err := s.targets.ValidateAndLock(ctx, tx, targetReferences); err != nil {
				return err
			}
			var err error
			if save, err = lockMenuRoomSave(ctx, tx, command, locale, nextItems); err != nil || !save.compiled.changed {
				return err
			}
			return s.checkpoints.RequireCurrentContributorsForPermission(
				ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU,
				command.MenuID, command.ContributorMemberIDs, menuRoomPermission(save.previous, save.compiled),
			)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (AIDocumentApplyResult, error) {
			if !save.compiled.changed {
				return AIDocumentApplyResult{
					DocumentRevision: save.previous.DocumentRevision,
					TargetRevision:   optional.Clone(save.previous.TargetRevision),
				}, nil
			}
			if err := s.persistAIDocument(ctx, tx, save.previous, save.compiled, time.Now().UTC()); err != nil {
				return AIDocumentApplyResult{}, err
			}
			reloaded, err := loadMenuAIDocumentSnapshot(ctx, tx, command.MenuID, locale, true)
			if err != nil {
				return AIDocumentApplyResult{}, err
			}
			return AIDocumentApplyResult{
				DocumentRevision: reloaded.DocumentRevision,
				TargetRevision:   optional.Clone(reloaded.TargetRevision),
				Changed:          true,
			}, nil
		},
		Audit: func(result AIDocumentApplyResult) mutation.Audit {
			if !result.Changed {
				return mutation.Unchanged()
			}
			return mutation.MemberAudit(contributor, sharedtelemetry.AuditMenuUpdated, menuDocumentAuditRecord(save.previous, save.compiled, false))
		},
	})
	if err != nil {
		return nil, err
	}
	return &intrav1.SaveMenuDocumentResponse{
		Locale: locale, DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision,
	}, nil
}

// lockMenuRoomSave locks the room, rejects a save against stale revisions
// or one a target room may not make, and compiles what the save changes.
func lockMenuRoomSave(ctx context.Context, tx *gorm.DB, command SaveMenuDocumentCommand, locale string, nextItems []AIDocumentItem) (menuRoomSave, error) {
	previous, err := loadMenuAIDocumentSnapshot(ctx, tx, command.MenuID, locale, true)
	if err != nil {
		return menuRoomSave{}, err
	}
	if err := requireCurrentMenuRoom(previous, command, locale); err != nil {
		return menuRoomSave{}, err
	}
	carryMenuRootLabels(nextItems, previous.Items)
	next := cloneMenuAIDocumentSnapshot(previous)
	next.Name = command.Name
	next.Items = nextItems
	next.Labels = cloneMenuLabels(command.RequestedLabels)

	isSource := locale == previous.SourceLocale
	if !isSource && (command.Name != previous.Name || !equalAIDocumentItems(previous.Items, nextItems)) {
		return menuRoomSave{}, domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Menu target room cannot change shared structure",
		)
	}
	if err := validateMenuCollaborationSnapshot(previous, next, isSource); err != nil {
		return menuRoomSave{}, err
	}
	sourceChanged := isSource && (previous.Name != next.Name || !equalAIDocumentItems(previous.Items, next.Items))
	labelsChanged := !menuLabelsEqual(previous.Labels, next.Labels)
	return menuRoomSave{previous: previous, compiled: compiledMenuAIDocument{
		snapshot: next, changed: sourceChanged || labelsChanged,
		sourceChanged:       sourceChanged,
		sourceValuesChanged: isSource && labelsChanged,
		targetChanged:       !isSource && labelsChanged,
	}}, nil
}

// requireCurrentMenuRoom rejects a save whose expected revisions no longer
// match the locked room.
func requireCurrentMenuRoom(previous AIDocumentSnapshot, command SaveMenuDocumentCommand, locale string) error {
	if previous.DocumentRevision != command.ExpectedDocumentRevision {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Menu document changed; reload before saving", domainerr.CollaborationRevisions{Document: previous.DocumentRevision},
		)
	}
	if locale == previous.SourceLocale {
		if command.ExpectedTargetRevision != nil {
			return domainerr.InvalidField("expected_target_revision", "source locale has no target revision")
		}
		return nil
	}
	if !previous.LocaleExists {
		return domainerr.Precondition("Menu target locale document does not exist")
	}
	if !optional.Equal(previous.TargetRevision, command.ExpectedTargetRevision) {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Menu target changed; reload before saving", domainerr.CollaborationRevisions{Document: previous.DocumentRevision, Target: *previous.TargetRevision, TargetExists: &previous.LocaleExists},
		)
	}
	return nil
}

// menuRoomPermission is Edit, or Manage when the save changes the item
// topology.
func menuRoomPermission(previous AIDocumentSnapshot, compiled compiledMenuAIDocument) intrav1.CollaborationPermission {
	if compiled.sourceChanged && !menuAIDocumentTopologyEqual(previous.Items, compiled.snapshot.Items) {
		return intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE
	}
	return intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT
}

func canonicalMenuRoomLocale(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	normalized := localization.NormalizeSupportedLocale(trimmed)
	if value != trimmed || normalized == nil || *normalized != trimmed {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return trimmed, nil
}

func requireCanonicalMenuCollaborationUUID(value, field string) error {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || parsed.String() != trimmed || value != trimmed {
		return domainerr.InvalidField(field, "must be a canonical UUID")
	}
	return nil
}

func canonicalMenuCollaborationContributor(values []string) (string, error) {
	if len(values) != 1 {
		return "", domainerr.InvalidField("contributor_member_ids", "requires exactly one origin Member")
	}
	if err := requireCanonicalMenuCollaborationUUID(values[0], "contributor_member_ids"); err != nil {
		return "", err
	}
	return values[0], nil
}

func menuCollaborationItems(items []AIDocumentItem) []*intrav1.MenuCollaborationItem {
	result := make([]*intrav1.MenuCollaborationItem, len(items))
	for index, item := range items {
		result[index] = &intrav1.MenuCollaborationItem{
			Id: item.ID, LinkType: item.LinkType,
			Url: cloneStringPointer(item.URL), TargetId: cloneStringPointer(item.TargetID),
			TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
			VisibilityMode:   cloneStringPointer(item.VisibilityMode),
			VisibilityRoles:  append([]string(nil), item.VisibilityRoles...),
			LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
			Children: menuCollaborationItems(item.Children),
		}
	}
	return result
}

func menuAIDocumentItems(items []*intrav1.MenuCollaborationItem) []AIDocumentItem {
	result := make([]AIDocumentItem, len(items))
	for index, item := range items {
		if item == nil {
			continue
		}
		result[index] = AIDocumentItem{
			ID: item.Id, LinkType: item.LinkType,
			URL: cloneStringPointer(item.Url), TargetID: cloneStringPointer(item.TargetId),
			TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
			VisibilityMode:   cloneStringPointer(item.VisibilityMode),
			VisibilityRoles:  append([]string(nil), item.VisibilityRoles...),
			LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
			Children: menuAIDocumentItems(item.Children),
		}
	}
	return result
}

func carryMenuRootLabels(next, current []AIDocumentItem) {
	labels := make(map[string]string)
	var collect func([]AIDocumentItem)
	collect = func(items []AIDocumentItem) {
		for _, item := range items {
			labels[item.ID] = item.Label
			collect(item.Children)
		}
	}
	collect(current)
	var apply func([]AIDocumentItem)
	apply = func(items []AIDocumentItem) {
		for index := range items {
			items[index].Label = labels[items[index].ID]
			apply(items[index].Children)
		}
	}
	apply(next)
}

func validateMenuCollaborationSnapshot(
	previous, next AIDocumentSnapshot,
	isSource bool,
) error {
	if isSource {
		if err := validateMenuName(next.Name); err != nil {
			return err
		}
		if err := validateAIDocumentItemConfiguration(next.Items); err != nil {
			return domainerr.InvalidField("items", err.Error())
		}
		validationItems := aiDocumentItemsToModel(next.Items)
		fillMissingMenuLabelsForStructuralValidation(validationItems)
		if err := validateMenuItems(modelItemsToProto(validationItems), 0); err != nil {
			return err
		}
	}
	if err := validateAIDocumentLocaleLabels(next.Labels); err != nil {
		return domainerr.InvalidField("requested_labels", err.Error())
	}
	items := make(map[string]AIDocumentItem)
	var collect func([]AIDocumentItem)
	collect = func(current []AIDocumentItem) {
		for _, item := range current {
			items[item.ID] = item
			collect(item.Children)
		}
	}
	collect(next.Items)
	for id := range next.Labels {
		item, exists := items[id]
		if !exists || !item.OwnsLabel(next.Locale) {
			return domainerr.InvalidField("requested_labels", "contains a label not owned by this locale")
		}
	}
	if !isSource && !previous.LocaleExists {
		return domainerr.Precondition("Menu target locale document does not exist")
	}
	return nil
}

func menuLabelsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for id, value := range left {
		if other, ok := right[id]; !ok || other != value {
			return false
		}
	}
	return true
}

func menuAIDocumentTopologyEqual(left, right []AIDocumentItem) bool {
	var encode func([]AIDocumentItem, string, *[]string)
	encode = func(items []AIDocumentItem, parent string, output *[]string) {
		for index, item := range items {
			*output = append(*output, parent+"/"+item.ID+"@"+strconv.Itoa(index))
			encode(item.Children, item.ID, output)
		}
	}
	var a, b []string
	encode(left, "", &a)
	encode(right, "", &b)
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

package menu

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// InternalMenuService owns persistence for one resident Menu source or
// existing target locale room. The collaboration document is only a transient
// projection of this aggregate.
type InternalMenuService struct {
	menus       *MenuService
	checkpoints MenuCollaborationCheckpointFence
}

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

func NewInternalMenuService(
	menus *MenuService,
	checkpoints MenuCollaborationCheckpointFence,
) *InternalMenuService {
	if menus == nil || checkpoints == nil {
		panic("Menu collaboration dependencies are required")
	}
	return &InternalMenuService{menus: menus, checkpoints: checkpoints}
}

func (s *InternalMenuService) LoadDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadMenuDocumentRequest],
) (*connect.Response[intrav1.LoadMenuDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	locale, err := canonicalMenuRoomLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := canonicalMenuCollaborationUUID(req.Msg.MenuId, "menu_id"); err != nil {
		return nil, err
	}

	var response *connect.Response[intrav1.LoadMenuDocumentResponse]
	err = s.menus.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := loadMenuAIDocumentSnapshot(ctx, tx, req.Msg.MenuId, locale, false)
		if err != nil {
			return err
		}
		response = connect.NewResponse(&intrav1.LoadMenuDocumentResponse{
			SourceLocale:     snapshot.SourceLocale,
			Locale:           snapshot.Locale,
			LocaleExists:     snapshot.LocaleExists,
			Name:             snapshot.Name,
			Items:            menuCollaborationItems(snapshot.Items),
			SourceLabels:     cloneMenuLabels(snapshot.sourceLabels),
			RequestedLabels:  cloneMenuLabels(snapshot.Labels),
			DocumentRevision: snapshot.DocumentRevision,
			TargetRevision:   cloneMenuRevision(snapshot.TargetRevision),
		})
		return nil
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return response, nil
}

func (s *InternalMenuService) SaveDocument(
	ctx context.Context,
	req *connect.Request[intrav1.SaveMenuDocumentRequest],
) (*connect.Response[intrav1.SaveMenuDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	r := req.Msg
	locale, err := canonicalMenuRoomLocale(r.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := canonicalMenuCollaborationUUID(r.MenuId, "menu_id"); err != nil {
		return nil, err
	}
	if _, err := canonicalMenuCollaborationUUID(r.ExpectedDocumentRevision, "expected_document_revision"); err != nil {
		return nil, err
	}
	contributor, err := canonicalMenuCollaborationContributor(r.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	nextItems := menuAIDocumentItems(r.Items)
	targetReferences := collectMenuTargetReferences(
		s.menus.modelItemsToProto(aiDocumentItemsToModel(nextItems)),
	)

	var result AIDocumentApplyResult
	err = s.menus.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Keep the domain-wide lock order used by direct Menu writes: concrete
		// targets first, then the owning Menu root and locale rows.
		if err := s.menus.targets.ValidateAndLock(ctx, tx, targetReferences); err != nil {
			return err
		}
		previous, err := loadMenuAIDocumentSnapshot(ctx, tx, r.MenuId, locale, true)
		if err != nil {
			return err
		}
		if previous.DocumentRevision != r.ExpectedDocumentRevision {
			return errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
				"Menu document changed; reload before saving",
			)
		}
		if locale != previous.SourceLocale {
			if !previous.LocaleExists {
				return errs.FailedPrecondition("Menu target locale document does not exist")
			}
			if !equalMenuRevision(previous.TargetRevision, r.ExpectedTargetRevision) {
				return errs.CollaborationConflict(
					intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
					"Menu target changed; reload before saving",
				)
			}
		} else if r.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source locale has no target revision")
		}

		carryMenuRootLabels(nextItems, previous.Items)
		next := cloneMenuAIDocumentSnapshot(previous)
		next.Name = r.Name
		next.Items = nextItems
		next.Labels = cloneMenuLabels(r.RequestedLabels)

		isSource := locale == previous.SourceLocale
		if !isSource {
			if r.Name != previous.Name || !equalAIDocumentItems(previous.Items, nextItems) {
				return errs.CollaborationMutationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
					"Menu target room cannot change shared structure",
				)
			}
		}
		if err := validateMenuCollaborationSnapshot(s.menus, previous, next, isSource); err != nil {
			return err
		}

		sourceChanged := isSource &&
			(previous.Name != next.Name || !equalAIDocumentItems(previous.Items, next.Items))
		labelsChanged := !menuLabelsEqual(previous.Labels, next.Labels)
		changed := sourceChanged || labelsChanged
		if !changed {
			result = AIDocumentApplyResult{
				DocumentRevision: previous.DocumentRevision,
				TargetRevision:   cloneMenuRevision(previous.TargetRevision),
			}
			return nil
		}

		permission := intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT
		if sourceChanged && !menuAIDocumentTopologyEqual(previous.Items, next.Items) {
			permission = intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE
		}
		if err := s.checkpoints.RequireCurrentContributorsForPermission(
			ctx, tx,
			intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU,
			r.MenuId, r.ContributorMemberIds, permission,
		); err != nil {
			return err
		}

		now := time.Now().UTC()
		compiled := compiledMenuAIDocument{
			snapshot: next, changed: true,
			sourceChanged:       sourceChanged,
			sourceValuesChanged: isSource && labelsChanged,
			targetChanged:       !isSource && labelsChanged,
		}
		if sourceChanged {
			if err := s.menus.persistAIDocumentSource(ctx, tx, previous, next, now); err != nil {
				return err
			}
		}
		if compiled.sourceValuesChanged {
			if err := persistMenuAIDocumentSourceValues(ctx, tx, previous, next, now); err != nil {
				return err
			}
		}
		if sourceChanged || compiled.sourceValuesChanged {
			expected, err := parseMenuContentDocumentUUID(previous.DocumentRevision, "content_document.revision")
			if err != nil {
				return err
			}
			if _, err := advanceMenuContentDocument(
				ctx, tx, previous.ID, previous.contentDocumentID, expected, now,
			); err != nil {
				return err
			}
		}
		if compiled.targetChanged {
			if err := persistMenuAIDocumentTarget(ctx, tx, previous, next, false, now); err != nil {
				return err
			}
		}
		if err := appendMenuCollaborationAudit(
			ctx, tx, s.menus.auditWriter, contributor, previous, compiled,
		); err != nil {
			return err
		}
		reloaded, err := loadMenuAIDocumentSnapshot(ctx, tx, r.MenuId, locale, true)
		if err != nil {
			return err
		}
		result = AIDocumentApplyResult{
			DocumentRevision: reloaded.DocumentRevision,
			TargetRevision:   cloneMenuRevision(reloaded.TargetRevision),
			Changed:          true,
		}
		return nil
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.SaveMenuDocumentResponse{
		Locale: locale, DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision,
	}), nil
}

func canonicalMenuRoomLocale(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	normalized := localization.NormalizeSupportedLocale(trimmed)
	if value != trimmed || normalized == nil || *normalized != trimmed {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return trimmed, nil
}

func canonicalMenuCollaborationUUID(value, field string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || parsed.String() != trimmed || value != trimmed {
		return uuid.Nil, errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return parsed, nil
}

func canonicalMenuCollaborationContributor(values []string) (string, error) {
	if len(values) != 1 {
		return "", errs.InvalidArgument("contributor_member_ids", "requires exactly one origin Member")
	}
	if _, err := canonicalMenuCollaborationUUID(values[0], "contributor_member_ids"); err != nil {
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
	service *MenuService,
	previous, next AIDocumentSnapshot,
	isSource bool,
) error {
	if isSource {
		if err := service.validateMenuName(next.Name); err != nil {
			return err
		}
		if err := validateAIDocumentItemConfiguration(next.Items); err != nil {
			return errs.InvalidArgument("items", err.Error())
		}
		validationItems := aiDocumentItemsToModel(next.Items)
		fillMissingMenuLabelsForStructuralValidation(validationItems)
		if err := service.validateMenuItems(service.modelItemsToProto(validationItems), 0); err != nil {
			return err
		}
	}
	if err := validateAIDocumentLocaleLabels(next.Labels); err != nil {
		return errs.InvalidArgument("requested_labels", err.Error())
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
			return errs.InvalidArgument("requested_labels", "contains a label not owned by this locale")
		}
	}
	if !isSource && !previous.LocaleExists {
		return errs.FailedPrecondition("Menu target locale document does not exist")
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

func appendMenuCollaborationAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	previous AIDocumentSnapshot,
	compiled compiledMenuAIDocument,
) error {
	return domainaudit.AppendMember(
		ctx, tx, writer, memberID, sharedtelemetry.AuditMenuUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			if compiled.targetChanged {
				return sharedtelemetry.NewMenuLocaleContentAuditRecord(
					metadata, previous.ID, previous.Locale, sharedtelemetry.AuditItemOperationUpdated,
				)
			}
			fields := make([]string, 0, 2)
			if previous.Name != compiled.snapshot.Name {
				fields = append(fields, "name")
			}
			if !equalAIDocumentItems(previous.Items, compiled.snapshot.Items) || compiled.sourceValuesChanged {
				fields = append(fields, "items")
			}
			sort.Strings(fields)
			return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, previous.ID, fields)
		},
	)
}

package collaborationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PermissionChecker interface {
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

type RootReader func(context.Context, *gorm.DB, string) (archived bool, found bool, err error)

type resourceAuthorizer struct {
	db       *gorm.DB
	checker  PermissionChecker
	can      collaborationCanSet
	readRoot RootReader
}

type collaborationCanSet struct {
	view         auth.ResourceAction
	edit         auth.ResourceAction
	manage       auth.ResourceAction
	viewArchived auth.ResourceAction
	editArchived auth.ResourceAction
}

func (set collaborationCanSet) forPermission(
	resourceID string,
	permission intrav1.CollaborationPermission,
	archived bool,
) (policyv1.Can, error) {
	var constructor auth.ResourceAction
	switch permission {
	case intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW:
		constructor = set.view
		if archived {
			constructor = set.viewArchived
		}
	case intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT:
		constructor = set.edit
		if archived {
			constructor = set.editArchived
		}
	case intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE:
		constructor = set.manage
	}
	if constructor == nil {
		return policyv1.Can{}, fmt.Errorf("unsupported collaboration permission %q", permission)
	}
	return constructor(resourceID)
}

func (a resourceAuthorizer) Authorize(
	ctx context.Context,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	if a.readRoot == nil {
		return false, errs.InternalMsg("collaboration resource root reader is not configured")
	}

	var allowed bool
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		archived, found, err := a.readRoot(ctx, tx, resourceID)
		if err != nil {
			return errs.Internal(fmt.Errorf("load collaboration resource root: %w", err))
		}
		if !found {
			return nil
		}

		can, err := a.can.forPermission(resourceID, permission, archived)
		if err != nil {
			return errs.InvalidArgument("permission", err.Error())
		}
		allowed, err = a.check(ctx, can, subject)
		return err
	})
	if err != nil {
		return false, err
	}
	return allowed, nil
}

func (a resourceAuthorizer) AuthorizeInTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	if tx == nil || a.readRoot == nil {
		return false, errs.InternalMsg("collaboration resource root reader is not configured")
	}
	archived, found, err := a.readRoot(ctx, tx, resourceID)
	if err != nil {
		return false, errs.Internal(fmt.Errorf("load collaboration resource root: %w", err))
	}
	if !found {
		return false, nil
	}
	can, err := a.can.forPermission(resourceID, permission, archived)
	if err != nil {
		return false, errs.InvalidArgument("permission", err.Error())
	}
	return a.check(ctx, can, subject)
}

func (a resourceAuthorizer) check(
	ctx context.Context,
	can policyv1.Can,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return false, errs.AuthenticationRequired()
	}
	allowed, err := a.checker.CheckActorCan(ctx, actor, can)
	if err != nil {
		return false, errs.DependencyUnavailable("SpiceDB")
	}
	return allowed, nil
}

func lifecycleReader(table, archivedStatus string) RootReader {
	return func(ctx context.Context, tx *gorm.DB, resourceID string) (bool, bool, error) {
		var row struct {
			Status string
		}
		err := tx.WithContext(ctx).
			Table(table).
			Clauses(clause.Locking{Strength: "SHARE"}).
			Select("status").
			Where("id = ?", resourceID).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		return row.Status == archivedStatus, true, nil
	}
}

func rootExistenceReader(table string) RootReader {
	return func(ctx context.Context, tx *gorm.DB, resourceID string) (bool, bool, error) {
		var row struct {
			ID string
		}
		err := tx.WithContext(ctx).
			Table(table).
			Clauses(clause.Locking{Strength: "SHARE"}).
			Select("id").
			Where("id = ?", resourceID).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		return false, true, nil
	}
}

type registrationSpec struct {
	resourceType   intrav1.CollaborationResourceType
	can            collaborationCanSet
	table          string
	archivedStatus string
}

var registrationSpecs = []registrationSpec{
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST, collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived}, "post", managev1.PostStatus_POST_STATUS_ARCHIVED.String()},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK, collaborationCanSet{view: policyv1.Work.View, edit: policyv1.Work.Edit, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived}, "work", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE, collaborationCanSet{view: policyv1.Release.View, edit: policyv1.Release.Edit}, "release", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_LABEL, collaborationCanSet{view: policyv1.Label.View, edit: policyv1.Label.Edit}, "label", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_ARTIST, collaborationCanSet{view: policyv1.Artist.View, edit: policyv1.Artist.Edit}, "artist", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_FORM, collaborationCanSet{view: policyv1.Form.View, edit: policyv1.Form.Edit}, "form", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE, collaborationCanSet{view: policyv1.Page.View, edit: policyv1.Page.Edit}, "page", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN, collaborationCanSet{view: policyv1.Campaign.View, edit: policyv1.Campaign.Edit}, "campaign", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE, collaborationCanSet{view: policyv1.EmailTemplate.View, edit: policyv1.EmailTemplate.Edit}, "email_template", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_LAYOUT, collaborationCanSet{view: policyv1.EmailLayout.View, edit: policyv1.EmailLayout.Edit}, "email_layout", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY, collaborationCanSet{view: policyv1.TermsHistory.View, edit: policyv1.TermsHistory.Edit, viewArchived: policyv1.TermsHistory.ViewArchived, editArchived: policyv1.TermsHistory.EditArchived}, "terms_history", managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY, collaborationCanSet{view: policyv1.PrivacyHistory.View, edit: policyv1.PrivacyHistory.Edit, viewArchived: policyv1.PrivacyHistory.ViewArchived, editArchived: policyv1.PrivacyHistory.EditArchived}, "privacy_history", managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MAP_THEME, collaborationCanSet{view: policyv1.MapTheme.View, edit: policyv1.MapTheme.Edit}, "map_theme", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT, collaborationCanSet{view: policyv1.ProgramEvent.View, edit: policyv1.ProgramEvent.Edit, viewArchived: policyv1.ProgramEvent.ViewArchived, editArchived: policyv1.ProgramEvent.EditArchived}, "program_event", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU, collaborationCanSet{view: policyv1.Menu.View, edit: policyv1.Menu.Edit, manage: policyv1.Menu.Manage}, "menu", ""},
	{intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES, collaborationCanSet{view: policyv1.PostSeries.View, edit: policyv1.PostSeries.Edit}, "series", ""},
}

func (spec registrationSpec) reader() RootReader {
	if spec.archivedStatus == "" {
		return rootExistenceReader(spec.table)
	}
	return lifecycleReader(spec.table, spec.archivedStatus)
}

type Runtime struct {
	Registry    *collaboration.Registry
	Members     collaboration.MemberLoader
	Checkpoints *collaboration.CheckpointFence
}

func NewRuntime(db *gorm.DB, checker PermissionChecker, cdnDomain string) *Runtime {
	if db == nil || checker == nil {
		panic("collaboration runtime adapter requires database and permission checker")
	}
	registry := NewRegistry(db, checker)
	return &Runtime{
		Registry:    registry,
		Members:     memberLoader{db: db, cdnDomain: cdnDomain},
		Checkpoints: collaboration.NewCheckpointFence(registry, contributorResolver{}),
	}
}

func NewRegistry(db *gorm.DB, checker PermissionChecker) *collaboration.Registry {
	if db == nil || checker == nil {
		panic("collaboration authorization registry requires database and permission checker")
	}
	registrations := make([]collaboration.Registration, 0, len(registrationSpecs))
	for _, spec := range registrationSpecs {
		registrations = append(registrations, registration(
			spec.resourceType,
			spec.can,
			db,
			checker,
			spec.reader(),
		))
	}
	return collaboration.NewRegistry(registrations...)
}

func NewCheckpointFence(db *gorm.DB, checker PermissionChecker) *collaboration.CheckpointFence {
	return collaboration.NewCheckpointFence(NewRegistry(db, checker), contributorResolver{})
}

func registration(
	resourceType intrav1.CollaborationResourceType,
	can collaborationCanSet,
	db *gorm.DB,
	checker PermissionChecker,
	readRoot RootReader,
) collaboration.Registration {
	return collaboration.Registration{
		ResourceType: resourceType,
		Authorizer: resourceAuthorizer{
			db:       db,
			checker:  checker,
			can:      can,
			readRoot: readRoot,
		},
	}
}

type memberLoader struct {
	db        *gorm.DB
	cdnDomain string
}

func (l memberLoader) LoadActiveSummary(
	ctx context.Context,
	memberID string,
) (*commonv1.MemberSummary, bool, error) {
	var row model.Member
	result := l.db.WithContext(ctx).
		Select("id", "nickname").
		Where("id = ? AND deleted_at IS NULL", memberID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if result.Error != nil {
		return nil, false, result.Error
	}
	summary := &commonv1.MemberSummary{Id: row.ID, Nickname: row.Nickname}
	type avatarRow struct{ model.PublicAsset }
	var avatar avatarRow
	assetResult := l.db.WithContext(ctx).Raw(`
		SELECT asset.*
		FROM public_asset_binding AS binding
		JOIN public_asset AS asset ON asset.id = binding.asset_id
		WHERE binding.owner_type = 'member'
		  AND binding.binding_key = 'avatar'
		  AND binding.owner_id = ?
		  AND asset.status = 'ready'
	`, memberID).Take(&avatar)
	if assetResult.Error != nil && !errors.Is(assetResult.Error, gorm.ErrRecordNotFound) {
		return nil, false, assetResult.Error
	}
	if assetResult.Error == nil {
		asset, err := mediaasset.NewLifecycle(l.db, l.cdnDomain).AssetRef(avatar.PublicAsset)
		if err != nil {
			return nil, false, err
		}
		summary.AvatarAsset = asset
	}
	return summary, true, nil
}

type contributorResolver struct{}

func (contributorResolver) ResolveActiveSubjects(
	ctx context.Context,
	tx *gorm.DB,
	memberIDs []string,
) (map[string]auth.AccountIdentitySubject, error) {
	type contributorRow struct {
		MemberID   string `gorm:"column:member_id"`
		IdentityID string `gorm:"column:account_identity_id"`
	}
	var rows []contributorRow
	if err := tx.WithContext(ctx).
		Table("member AS member").
		Joins("JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.state = ?", "active").
		Select("member.id::text AS member_id, member.account_identity_id::text AS account_identity_id").
		Where("member.id IN ? AND member.deleted_at IS NULL AND member.account_identity_id IS NOT NULL", memberIDs).
		Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	subjects := make(map[string]auth.AccountIdentitySubject, len(rows))
	for _, row := range rows {
		subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(row.IdentityID))
		if err != nil {
			continue
		}
		subjects[row.MemberID] = subject
	}
	return subjects, nil
}

package sharelinkadapter

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type Authority struct {
	db          *gorm.DB
	spiceDB     resourcePermissionChecker
	auditWriter domainaudit.Appender
}

type resourcePermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

func NewAuthority(db *gorm.DB, spiceDB *auth.SpiceDBClient, auditWriter domainaudit.Appender) *Authority {
	if spiceDB == nil {
		panic("sharelink adapter: spiceDB is required")
	}
	return newAuthority(db, spiceDB, auditWriter)
}

func newAuthority(db *gorm.DB, spiceDB resourcePermissionChecker, auditWriter domainaudit.Appender) *Authority {
	if db == nil {
		panic("sharelink adapter: db is required")
	}
	if spiceDB == nil {
		panic("sharelink adapter: spiceDB is required")
	}
	return &Authority{db: db, spiceDB: spiceDB, auditWriter: auditWriter}
}

type target struct {
	table                string
	name                 string
	entityType           managev1.ShareLinkEntityType
	archivedStatus       string
	legalScheduledStatus string
	manageShareLinks     auth.ResourceAction
	viewArchived         auth.ResourceAction
	editArchived         auth.ResourceAction
}

func targetFor(entityType managev1.ShareLinkEntityType) (target, error) {
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST:
		return target{
			table:            "post",
			name:             "post",
			entityType:       entityType,
			archivedStatus:   managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
			manageShareLinks: policyv1.Post.ManageShareLinks,
			viewArchived:     policyv1.Post.ViewArchived,
			editArchived:     policyv1.Post.EditArchived,
		}, nil
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE:
		return target{table: "page", name: "page", entityType: entityType, manageShareLinks: policyv1.Page.ManageShareLinks}, nil
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK:
		return target{
			table:            "work",
			name:             "work",
			entityType:       entityType,
			archivedStatus:   managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
			manageShareLinks: policyv1.Work.ManageShareLinks,
			viewArchived:     policyv1.Work.ViewArchived,
			editArchived:     policyv1.Work.EditArchived,
		}, nil
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD:
		return target{table: "form", name: "form", entityType: entityType, manageShareLinks: policyv1.Form.ManageShareLinks}, nil
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY:
		return target{
			table:                "privacy_history",
			name:                 "privacy",
			entityType:           entityType,
			archivedStatus:       managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
			legalScheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			manageShareLinks:     policyv1.PrivacyHistory.ManageShareLinks,
			viewArchived:         policyv1.PrivacyHistory.ViewArchived,
			editArchived:         policyv1.PrivacyHistory.EditArchived,
		}, nil
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
		return target{
			table:                "terms_history",
			name:                 "terms",
			entityType:           entityType,
			archivedStatus:       managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
			legalScheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			manageShareLinks:     policyv1.TermsHistory.ManageShareLinks,
			viewArchived:         policyv1.TermsHistory.ViewArchived,
			editArchived:         policyv1.TermsHistory.EditArchived,
		}, nil
	default:
		return target{}, errs.InvalidEntityType(entityType.String())
	}
}

type authorizationUse uint8

const (
	authorizationRead authorizationUse = iota
	authorizationMutation
)

type targetState struct {
	ID            string     `gorm:"column:id"`
	Status        string     `gorm:"column:status"`
	EffectiveFrom *time.Time `gorm:"column:effective_from"`
}

func (a *Authority) AuthorizeList(ctx context.Context, entityType managev1.ShareLinkEntityType, entityID string) error {
	target, err := targetFor(entityType)
	if err != nil {
		return err
	}
	state, err := loadTargetState(ctx, a.db, target, entityID, false)
	if err != nil {
		return err
	}
	if err := a.requirePermission(ctx, target, entityID, targetAction(target, state, authorizationRead)); err != nil {
		return err
	}
	return requireTargetLifecycle(target, state)
}

func (a *Authority) Create(ctx context.Context, entityType managev1.ShareLinkEntityType, entityID string, link *model.ShareLink, create sharelink.CreateRecord) error {
	target, err := targetFor(entityType)
	if err != nil {
		return err
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := loadTargetState(ctx, tx, target, entityID, true)
		if err != nil {
			return err
		}
		if err := a.requirePermission(ctx, target, entityID, targetAction(target, state, authorizationMutation)); err != nil {
			return err
		}
		if err := requireTargetLifecycle(target, state); err != nil {
			return err
		}
		if err := create(ctx, tx, link); err != nil {
			return err
		}
		return a.appendAudit(ctx, tx, entityType, entityID, link.ID, sharedtelemetry.AuditItemOperationCreated)
	})
}

func (a *Authority) Delete(ctx context.Context, entityType managev1.ShareLinkEntityType, link model.ShareLink, deleteRecord sharelink.DeleteRecord) error {
	target, err := targetFor(entityType)
	if err != nil {
		return err
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := loadTargetState(ctx, tx, target, link.EntityID, true)
		if err != nil {
			return err
		}
		if err := a.requirePermission(ctx, target, link.EntityID, targetAction(target, state, authorizationMutation)); err != nil {
			return err
		}
		if err := requireTargetLifecycle(target, state); err != nil {
			return err
		}
		if err := deleteRecord(ctx, tx, link); err != nil {
			return err
		}
		return a.appendAudit(ctx, tx, entityType, link.EntityID, link.ID, sharedtelemetry.AuditItemOperationDeleted)
	})
}

func loadTargetState(ctx context.Context, db *gorm.DB, target target, entityID string, lock bool) (targetState, error) {
	columns := []string{"id"}
	if target.archivedStatus != "" {
		columns = append(columns, "status")
	}
	if target.legalScheduledStatus != "" {
		columns = append(columns, "effective_from")
	}
	var state targetState
	query := db.WithContext(ctx).Table(target.table).Select(columns).Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return targetState{}, errs.NotFound(target.name, entityID)
		}
		return targetState{}, errs.Internal(err)
	}
	return state, nil
}

func targetAction(target target, state targetState, use authorizationUse) auth.ResourceAction {
	if target.archivedStatus == "" || state.Status != target.archivedStatus {
		return target.manageShareLinks
	}
	if use == authorizationRead {
		return target.viewArchived
	}
	return target.editArchived
}

func (a *Authority) requirePermission(ctx context.Context, target target, entityID string, action auth.ResourceAction) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if principal.Banned {
		return errs.AccountBanned()
	}
	if action == nil {
		return errs.InvalidArgument("entity_id", "unsupported Share Link action")
	}
	can, err := action(entityID)
	if err != nil {
		return errs.InvalidArgument("entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, checkErr := a.spiceDB.Can(ctx, decision)
	if checkErr != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound(target.name, entityID)
	}
	return nil
}

func requireTargetLifecycle(target target, state targetState) error {
	if target.legalScheduledStatus == "" {
		return nil
	}
	if state.Status != target.legalScheduledStatus || state.EffectiveFrom == nil || !state.EffectiveFrom.After(time.Now()) {
		return errs.FailedPrecondition(target.name + " preview links require a future scheduled version")
	}
	return nil
}

func (a *Authority) appendAudit(ctx context.Context, tx *gorm.DB, entityType managev1.ShareLinkEntityType, entityID, linkID string, operation sharedtelemetry.AuditItemOperation) error {
	if a.auditWriter == nil {
		return nil
	}
	var action sharedtelemetry.AuditAction
	var build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST:
		action = sharedtelemetry.AuditPostUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostShareLinkAuditRecord(m, entityID, linkID, operation)
		}
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE:
		action = sharedtelemetry.AuditPageUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageShareLinkAuditRecord(m, entityID, linkID, operation)
		}
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK:
		action = sharedtelemetry.AuditWorkUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkShareLinkAuditRecord(m, entityID, linkID, operation)
		}
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD:
		action = sharedtelemetry.AuditFormUpdated
		scope := sharedtelemetry.AuditItemScopeForm
		if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD {
			scope = sharedtelemetry.AuditItemScopeDashboard
		}
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormShareLinkAuditRecord(m, entityID, linkID, scope, operation)
		}
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
		return a.appendLegalAudit(ctx, tx, entityType, entityID, linkID, operation)
	default:
		return errs.InvalidEntityType(entityType.String())
	}
	return domainaudit.AppendRequest(ctx, tx, a.auditWriter, action, build)
}

func (a *Authority) appendLegalAudit(ctx context.Context, tx *gorm.DB, entityType managev1.ShareLinkEntityType, entityID, linkID string, operation sharedtelemetry.AuditItemOperation) error {
	table, name, policyType := "terms_history", "terms", sharedtelemetry.AuditPolicyTypeTerms
	if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY {
		table, name, policyType = "privacy_history", "privacy", sharedtelemetry.AuditPolicyTypePrivacy
	}
	var row struct {
		Version int64 `gorm:"column:version"`
	}
	if err := tx.WithContext(ctx).Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Select("version").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound(name, entityID)
		}
		return errs.Internal(err)
	}
	return domainaudit.AppendRequest(ctx, tx, a.auditWriter, sharedtelemetry.AuditLegalPolicyUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLegalPolicyShareLinkAuditRecord(m, entityID, policyType, row.Version, operation, linkID)
	})
}

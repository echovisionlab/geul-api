package sharelinkadapter

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Authority resolves Share Link targets for the Share Link use cases.
type Authority struct {
	db      *gorm.DB
	spiceDB resourcePermissionChecker
}

type resourcePermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

func NewAuthority(db *gorm.DB, spiceDB *auth.SpiceDBClient) *Authority {
	if spiceDB == nil {
		panic("sharelink adapter: spiceDB is required")
	}
	return newAuthority(db, spiceDB)
}

func newAuthority(db *gorm.DB, spiceDB resourcePermissionChecker) *Authority {
	if db == nil {
		panic("sharelink adapter: db is required")
	}
	if spiceDB == nil {
		panic("sharelink adapter: spiceDB is required")
	}
	return &Authority{db: db, spiceDB: spiceDB}
}

type target struct {
	table                string
	name                 string
	entityType           sharelink.EntityType
	archivedStatus       string
	legalScheduledStatus string
	manageShareLinks     auth.ResourceAction
	viewArchived         auth.ResourceAction
	editArchived         auth.ResourceAction
}

func targetFor(entityType sharelink.EntityType) (target, error) {
	switch entityType {
	case sharelink.EntityPost:
		return target{
			table:            "post",
			name:             "post",
			entityType:       entityType,
			archivedStatus:   managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
			manageShareLinks: policyv1.Post.ManageShareLinks,
			viewArchived:     policyv1.Post.ViewArchived,
			editArchived:     policyv1.Post.EditArchived,
		}, nil
	case sharelink.EntityPage:
		return target{table: "page", name: "page", entityType: entityType, manageShareLinks: policyv1.Page.ManageShareLinks}, nil
	case sharelink.EntityWork:
		return target{
			table:            "work",
			name:             "work",
			entityType:       entityType,
			archivedStatus:   managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
			manageShareLinks: policyv1.Work.ManageShareLinks,
			viewArchived:     policyv1.Work.ViewArchived,
			editArchived:     policyv1.Work.EditArchived,
		}, nil
	case sharelink.EntityRelease:
		return target{table: "release", name: "release", entityType: entityType, manageShareLinks: policyv1.Release.ManageShareLinks}, nil
	case sharelink.EntityForm, sharelink.EntityFormDashboard:
		return target{table: "form", name: "form", entityType: entityType, manageShareLinks: policyv1.Form.ManageShareLinks}, nil
	case sharelink.EntityLabel:
		return target{table: "label", name: "label", entityType: entityType, manageShareLinks: policyv1.Label.ManageShareLinks}, nil
	case sharelink.EntityArtist:
		return target{table: "artist", name: "artist", entityType: entityType, manageShareLinks: policyv1.Artist.ManageShareLinks}, nil
	case sharelink.EntityPrivacy:
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
	case sharelink.EntityTerms:
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
		return target{}, errs.InvalidEntityType(string(entityType))
	}
}

type authorizationUse uint8

const (
	authorizationRead authorizationUse = iota
	authorizationMutation
)

type targetState struct {
	ID            string     `gorm:"column:id"`
	Version       int64      `gorm:"column:version"`
	Status        string     `gorm:"column:status"`
	EffectiveFrom *time.Time `gorm:"column:effective_from"`
}

func (a *Authority) AuthorizeList(ctx context.Context, entityType sharelink.EntityType, entityID string) error {
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

// LockTarget locks the target row and selects the action its locked
// lifecycle requires of a link mutation.
func (a *Authority) LockTarget(ctx context.Context, tx *gorm.DB, entityType sharelink.EntityType, entityID string) (sharelink.LockedTarget, error) {
	target, err := targetFor(entityType)
	if err != nil {
		return sharelink.LockedTarget{}, err
	}
	state, err := loadTargetState(ctx, tx, target, entityID, true)
	if err != nil {
		return sharelink.LockedTarget{}, err
	}
	can, err := targetAction(target, state, authorizationMutation)(entityID)
	if err != nil {
		return sharelink.LockedTarget{}, errs.InvalidArgument("entity_id", err.Error())
	}
	return sharelink.LockedTarget{
		Name:      target.name,
		Can:       can,
		Lifecycle: requireTargetLifecycle(target, state),
		Audit: func(linkID string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
			return linkAudit(entityType, entityID, linkID, state.Version, operation)
		},
	}, nil
}

func loadTargetState(ctx context.Context, db *gorm.DB, target target, entityID string, lock bool) (targetState, error) {
	columns := []string{"id"}
	if target.archivedStatus != "" {
		columns = append(columns, "status")
	}
	if target.legalScheduledStatus != "" {
		columns = append(columns, "effective_from", "version")
	}
	var state targetState
	query := db.WithContext(ctx).Table(target.table).Select(columns).Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
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
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, checkErr := a.spiceDB.Can(ctx, decision)
	if checkErr != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), checkErr)
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

// linkAudit names the target's record of a link change. A legal policy
// records the version its locked row holds.
func linkAudit(entityType sharelink.EntityType, entityID, linkID string, version int64, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	audit := func(action sharedtelemetry.AuditAction, build func(sharedtelemetry.AuditMetadata, string, string, sharedtelemetry.AuditItemOperation) (sharedtelemetry.AuditRecord, error)) mutation.Audit {
		return mutation.RequestAudit(action, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return build(m, entityID, linkID, operation)
		})
	}
	switch entityType {
	case sharelink.EntityPost:
		return audit(sharedtelemetry.AuditPostUpdated, sharedtelemetry.NewPostShareLinkAuditRecord)
	case sharelink.EntityPage:
		return audit(sharedtelemetry.AuditPageUpdated, sharedtelemetry.NewPageShareLinkAuditRecord)
	case sharelink.EntityWork:
		return audit(sharedtelemetry.AuditWorkUpdated, sharedtelemetry.NewWorkShareLinkAuditRecord)
	case sharelink.EntityRelease:
		return audit(sharedtelemetry.AuditReleaseUpdated, sharedtelemetry.NewReleaseShareLinkAuditRecord)
	case sharelink.EntityArtist:
		return audit(sharedtelemetry.AuditArtistUpdated, sharedtelemetry.NewArtistShareLinkAuditRecord)
	case sharelink.EntityLabel:
		return audit(sharedtelemetry.AuditLabelUpdated, sharedtelemetry.NewLabelShareLinkAuditRecord)
	case sharelink.EntityForm, sharelink.EntityFormDashboard:
		scope := sharedtelemetry.AuditItemScopeForm
		if entityType == sharelink.EntityFormDashboard {
			scope = sharedtelemetry.AuditItemScopeDashboard
		}
		return mutation.RequestAudit(sharedtelemetry.AuditFormUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormShareLinkAuditRecord(m, entityID, linkID, scope, operation)
		})
	default:
		policyType := sharedtelemetry.AuditPolicyTypeTerms
		if entityType == sharelink.EntityPrivacy {
			policyType = sharedtelemetry.AuditPolicyTypePrivacy
		}
		return mutation.RequestAudit(sharedtelemetry.AuditLegalPolicyUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewLegalPolicyShareLinkAuditRecord(m, entityID, policyType, version, operation, linkID)
		})
	}
}

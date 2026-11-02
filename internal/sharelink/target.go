package sharelink

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// targetPolicy is what a link of one target kind requires: the actions that
// authorize it, the lifecycle a legal version must be in, and the record a
// link change appends to the target's audit.
type targetPolicy struct {
	// name is the resource a masked denial answers not found for.
	name             string
	manageShareLinks auth.ResourceAction
	// viewArchived and editArchived replace manageShareLinks while the
	// target is archived; nil for a target without an archived lifecycle.
	viewArchived auth.ResourceAction
	editArchived auth.ResourceAction
	// legal targets take links only while a future version is scheduled.
	legal bool
}

var targetPolicies = map[EntityType]targetPolicy{
	EntityPost: {
		name: "post", manageShareLinks: policyv1.Post.ManageShareLinks,
		viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived,
	},
	EntityPage:    {name: "page", manageShareLinks: policyv1.Page.ManageShareLinks},
	EntityWork:    {name: "work", manageShareLinks: policyv1.Work.ManageShareLinks, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived},
	EntityRelease: {name: "release", manageShareLinks: policyv1.Release.ManageShareLinks},
	EntityForm:    {name: "form", manageShareLinks: policyv1.Form.ManageShareLinks},
	EntityFormDashboard: {
		name: "form", manageShareLinks: policyv1.Form.ManageShareLinks,
	},
	EntityLabel:  {name: "label", manageShareLinks: policyv1.Label.ManageShareLinks},
	EntityArtist: {name: "artist", manageShareLinks: policyv1.Artist.ManageShareLinks},
	EntityPrivacy: {
		name: "privacy", manageShareLinks: policyv1.PrivacyHistory.ManageShareLinks,
		viewArchived: policyv1.PrivacyHistory.ViewArchived, editArchived: policyv1.PrivacyHistory.EditArchived, legal: true,
	},
	EntityTerms: {
		name: "terms", manageShareLinks: policyv1.TermsHistory.ManageShareLinks,
		viewArchived: policyv1.TermsHistory.ViewArchived, editArchived: policyv1.TermsHistory.EditArchived, legal: true,
	},
}

type authorizationUse uint8

const (
	authorizationRead authorizationUse = iota
	authorizationMutation
)

// action selects what the target's state requires of a link read or change.
func (p targetPolicy) action(target Target, use authorizationUse) auth.ResourceAction {
	if p.viewArchived == nil || !target.Archived {
		return p.manageShareLinks
	}
	if use == authorizationRead {
		return p.viewArchived
	}
	return p.editArchived
}

// lifecycle rejects a legal target whose version is not scheduled to take
// effect in the future. It is answered after authorization so it reveals
// nothing to a stranger.
func (p targetPolicy) lifecycle(target Target, now time.Time) error {
	if !p.legal {
		return nil
	}
	if !target.Scheduled || target.EffectiveFrom == nil || !target.EffectiveFrom.After(now) {
		return domainerr.Precondition(p.name + " preview links require a future scheduled version")
	}
	return nil
}

// authorizeRead requires that the caller may read the target's links. It
// checks the action without the identity fence, as every read does, and
// masks a denial as a missing target.
func (s *Service) authorizeRead(ctx context.Context, policy targetPolicy, entityID string, action auth.ResourceAction) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return domainerr.AccountBanned()
	}
	if action == nil {
		return domainerr.InvalidField("entity_id", "unsupported Share Link action")
	}
	can, err := action(entityID)
	if err != nil {
		return domainerr.InvalidField("entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.ResourceNotFound(policy.name, entityID)
	}
	return nil
}

// linkAudit names the target's record of a link change. A legal policy
// records the version its locked row holds.
func linkAudit(entityType EntityType, entityID, linkID string, version int64, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	audit := func(action sharedtelemetry.AuditAction, build func(sharedtelemetry.AuditMetadata, string, string, sharedtelemetry.AuditItemOperation) (sharedtelemetry.AuditRecord, error)) mutation.Audit {
		return mutation.RequestAudit(action, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return build(m, entityID, linkID, operation)
		})
	}
	switch entityType {
	case EntityPost:
		return audit(sharedtelemetry.AuditPostUpdated, sharedtelemetry.NewPostShareLinkAuditRecord)
	case EntityPage:
		return audit(sharedtelemetry.AuditPageUpdated, sharedtelemetry.NewPageShareLinkAuditRecord)
	case EntityWork:
		return audit(sharedtelemetry.AuditWorkUpdated, sharedtelemetry.NewWorkShareLinkAuditRecord)
	case EntityRelease:
		return audit(sharedtelemetry.AuditReleaseUpdated, sharedtelemetry.NewReleaseShareLinkAuditRecord)
	case EntityArtist:
		return audit(sharedtelemetry.AuditArtistUpdated, sharedtelemetry.NewArtistShareLinkAuditRecord)
	case EntityLabel:
		return audit(sharedtelemetry.AuditLabelUpdated, sharedtelemetry.NewLabelShareLinkAuditRecord)
	case EntityForm, EntityFormDashboard:
		scope := sharedtelemetry.AuditItemScopeForm
		if entityType == EntityFormDashboard {
			scope = sharedtelemetry.AuditItemScopeDashboard
		}
		return mutation.RequestAudit(sharedtelemetry.AuditFormUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormShareLinkAuditRecord(m, entityID, linkID, scope, operation)
		})
	default:
		policyType := sharedtelemetry.AuditPolicyTypeTerms
		if entityType == EntityPrivacy {
			policyType = sharedtelemetry.AuditPolicyTypePrivacy
		}
		return mutation.RequestAudit(sharedtelemetry.AuditLegalPolicyUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewLegalPolicyShareLinkAuditRecord(m, entityID, policyType, version, operation, linkID)
		})
	}
}

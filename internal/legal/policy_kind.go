package legal

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/email"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// PolicyKind names a legal policy kind. Terms and Privacy share every use
// case; the contracts keep separate RPCs and messages per kind, which the
// Connect translators and MCP tools map.
type PolicyKind string

const (
	Terms   PolicyKind = "terms"
	Privacy PolicyKind = "privacy"
)

// policyKind describes what differs between the kinds: storage, policy
// descriptors, notice events and the client wording each kind has always
// answered with.
type policyKind struct {
	kind      PolicyKind
	draft     Status
	scheduled Status
	active    Status
	archived  Status
	// shareLinkEntityType is the stored entity type of the kind's preview
	// share links.
	shareLinkEntityType string
	updateEvent         email.EventKey
	effectiveEvent      email.EventKey
	// effectiveURLKey is the effective notice's template variable that
	// links to the public route.
	effectiveURLKey string
	defaultTitle    string
	// notFoundResource names the kind in the NotFound answers of a version
	// read, schedule, cancel and delete.
	notFoundResource   string
	scheduleNonDraft   string
	cancelNonScheduled string
	list               func() (policyv1.Can, error)
	create             func() (policyv1.Can, error)
	touchPolicy        func(string) (policyv1.RelationshipMutation, error)
	deletePolicy       func(string) (policyv1.RelationshipMutation, error)
}

var policyKinds = map[PolicyKind]*policyKind{
	Terms: {
		kind:  Terms,
		draft: TermsDraft, scheduled: TermsScheduled, active: TermsActive, archived: TermsArchived,
		shareLinkEntityType: "SHARE_LINK_ENTITY_TYPE_TERMS",
		updateEvent:         email.EventTermsUpdate, effectiveEvent: email.EventTermsEffective, effectiveURLKey: "terms_url",
		defaultTitle: "Terms of Service", notFoundResource: "terms",
		scheduleNonDraft: "can only schedule draft terms", cancelNonScheduled: "can only cancel scheduled terms",
		list: policyv1.TermsHistory.List, create: policyv1.TermsHistory.Create,
		touchPolicy: policyv1.TermsHistory.TouchPolicy, deletePolicy: policyv1.TermsHistory.DeletePolicy,
	},
	Privacy: {
		kind:  Privacy,
		draft: PrivacyDraft, scheduled: PrivacyScheduled, active: PrivacyActive, archived: PrivacyArchived,
		shareLinkEntityType: "SHARE_LINK_ENTITY_TYPE_PRIVACY",
		updateEvent:         email.EventPrivacyUpdate, effectiveEvent: email.EventPrivacyEffective, effectiveURLKey: "privacy_url",
		defaultTitle: "Privacy Policy", notFoundResource: "privacy policy",
		scheduleNonDraft: "can only schedule draft privacy policies", cancelNonScheduled: "can only cancel scheduled privacy policies",
		list: policyv1.PrivacyHistory.List, create: policyv1.PrivacyHistory.Create,
		touchPolicy: policyv1.PrivacyHistory.TouchPolicy, deletePolicy: policyv1.PrivacyHistory.DeletePolicy,
	},
}

func (k PolicyKind) descriptor() (*policyKind, error) {
	descriptor, ok := policyKinds[k]
	if !ok {
		return nil, fmt.Errorf("unsupported legal policy kind %q", k)
	}
	return descriptor, nil
}

func (k *policyKind) entity() string { return string(k.kind) }

// ShareLinkEntityType returns the stored entity type of the kind's Share
// Links.
func (k PolicyKind) ShareLinkEntityType() (string, bool) {
	descriptor, ok := policyKinds[k]
	if !ok {
		return "", false
	}
	return descriptor.shareLinkEntityType, true
}

// PolicyStatus returns the stored status of kind in lifecycle state, one of
// draft, scheduled, active and archived.
func PolicyStatus(kind PolicyKind, state string) (Status, bool) {
	descriptor, ok := policyKinds[kind]
	if !ok {
		return "", false
	}
	switch state {
	case "draft":
		return descriptor.draft, true
	case "scheduled":
		return descriptor.scheduled, true
	case "active":
		return descriptor.active, true
	case "archived":
		return descriptor.archived, true
	default:
		return "", false
	}
}

// auditState names status in audit records; a status of another kind has
// none.
func (k *policyKind) auditState(status Status) sharedtelemetry.AuditState {
	switch status {
	case k.draft:
		return sharedtelemetry.AuditStateDraft
	case k.scheduled:
		return sharedtelemetry.AuditStateScheduled
	case k.active:
		return sharedtelemetry.AuditStateActive
	case k.archived:
		return sharedtelemetry.AuditStateArchived
	default:
		return ""
	}
}

package legal

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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
	kind PolicyKind
	// table holds the versions.
	table     string
	draft     model.LegalPolicyStatus
	scheduled model.LegalPolicyStatus
	active    model.LegalPolicyStatus
	archived  model.LegalPolicyStatus
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
		kind: Terms, table: "terms_history",
		draft: model.TermsStatusDraft, scheduled: model.TermsStatusScheduled,
		active: model.TermsStatusActive, archived: model.TermsStatusArchived,
		shareLinkEntityType: "SHARE_LINK_ENTITY_TYPE_TERMS",
		updateEvent:         email.EventTermsUpdate, effectiveEvent: email.EventTermsEffective, effectiveURLKey: "terms_url",
		defaultTitle: "Terms of Service", notFoundResource: "terms",
		scheduleNonDraft: "can only schedule draft terms", cancelNonScheduled: "can only cancel scheduled terms",
		list: policyv1.TermsHistory.List, create: policyv1.TermsHistory.Create,
		touchPolicy: policyv1.TermsHistory.TouchPolicy, deletePolicy: policyv1.TermsHistory.DeletePolicy,
	},
	Privacy: {
		kind: Privacy, table: "privacy_history",
		draft: model.PrivacyStatusDraft, scheduled: model.PrivacyStatusScheduled,
		active: model.PrivacyStatusActive, archived: model.PrivacyStatusArchived,
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

// PolicyStatus returns the stored status of kind in lifecycle state, one of
// draft, scheduled, active and archived.
func PolicyStatus(kind PolicyKind, state string) (model.LegalPolicyStatus, bool) {
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

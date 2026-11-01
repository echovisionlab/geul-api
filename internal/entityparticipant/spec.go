// Package entityparticipant holds the mechanism that Artist and Label share as
// owner/manager-governed catalog entities: participants, their SpiceDB
// relationships, the parent hierarchy, and the authorization checks around
// them. Each domain describes itself with a Spec and keeps its own RPC surface.
package entityparticipant

import (
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Participant roles as stored in the owner and manager tables.
const (
	RoleOwner   = "owner"
	RoleManager = "manager"
	RoleNone    = ""
)

// Policy is the generated SpiceDB domain of a participant-governed entity.
// policyv1.Artist and policyv1.Label satisfy it.
type Policy interface {
	Manage(id string) (policyv1.Can, error)
	ManageParticipants(id string) (policyv1.Can, error)
	RemoveOwner(id string) (policyv1.Can, error)
	Create() (policyv1.Can, error)
	List() (policyv1.Can, error)
	TouchPolicy(id string) (policyv1.RelationshipMutation, error)
	DeletePolicy(id string) (policyv1.RelationshipMutation, error)
	TouchOwner(id string, actor policyv1.Actor) (policyv1.RelationshipMutation, error)
	DeleteOwner(id string, actor policyv1.Actor) (policyv1.RelationshipMutation, error)
	TouchManager(id string, actor policyv1.Actor) (policyv1.RelationshipMutation, error)
	DeleteManager(id string, actor policyv1.Actor) (policyv1.RelationshipMutation, error)
	TouchParent(id string, parentID string) (policyv1.RelationshipMutation, error)
	DeleteParent(id string, parentID string) (policyv1.RelationshipMutation, error)
}

// Spec describes one participant-governed entity. Client-visible messages are
// built from Name, Title and Article, so each domain keeps its wording.
type Spec struct {
	// Name is the lower-case entity name used in not-found errors and
	// internal error text ("artist").
	Name string
	// Title is the capitalized entity name used in client messages ("Artist").
	Title string
	// Article is Title with its indefinite article ("an Artist").
	Article string

	// IDColumn names the entity ID in failure attributes ("artist_id"), and
	// ParentColumn the parent field in validation errors ("parent_artist_id").
	IDColumn     string
	ParentColumn string

	Policy Policy
}

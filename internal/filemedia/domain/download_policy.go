package domain

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

var ErrPolicyRelationNotFound = errors.New("file download policy relation not found")

type PolicySelector struct {
	OwnerKind, EntityID    string
	BlockID, ReferencePath *string
}
type PolicyRelation struct {
	Selector                 PolicySelector
	FileID                   string
	Access                   mediaasset.FileDownloadAccess
	ResourceType, ResourceID string
}
type PolicyCriteria struct {
	ID, Name, Kind string
	Description    *string
	Eligible       bool
	ArchivedAt     *time.Time
}
type PolicyChangeSignal struct {
	ResourceType, ResourceID string
	TimestampMS              int64
}
type DownloadPolicyStore interface {
	ResolveDownloadPolicyRelation(context.Context, PolicySelector, bool) (PolicyRelation, error)
	DownloadPolicyCriteriaIDs(context.Context, PolicySelector) ([]string, error)
	SetDownloadPolicy(context.Context, PolicySelector, mediaasset.FileDownloadAccess, []string, time.Time) error
	ActivePolicyCriteriaSet(context.Context, []string) ([]PolicyCriteria, error)
	ValidatePolicyCriteriaSet(context.Context, []string) error
	RequirePolicyOwner(context.Context, string, string, bool) error
	PublishPolicyChange(context.Context, PolicyChangeSignal) error
}

type DownloadPolicy struct {
	EntityType             int32
	EntityID               string
	BlockID, ReferencePath *string
	FileID                 string
	Access                 mediaasset.FileDownloadAccess
	MemberCriteria         []*PolicyCriteria
}

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
	Audience                 mediaasset.FileDownloadAudience
	ResourceType, ResourceID string
}
type PolicySegment struct {
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
	DownloadPolicySegmentIDs(context.Context, PolicySelector) ([]string, error)
	SetDownloadPolicy(context.Context, PolicySelector, mediaasset.FileDownloadAudience, []string, time.Time) error
	ActivePolicySegments(context.Context, []string) ([]PolicySegment, error)
	ValidatePolicySegments(context.Context, []string) error
	RequirePolicyOwner(context.Context, string, string, bool) error
	PublishPolicyChange(context.Context, PolicyChangeSignal) error
}

type DownloadPolicy struct {
	EntityType             int32
	EntityID               string
	BlockID, ReferencePath *string
	FileID                 string
	Audience               mediaasset.FileDownloadAudience
	AudienceSegments       []*PolicySegment
}

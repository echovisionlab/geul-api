package sharelink

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TargetAuthority is the Share Link view of the target another domain owns:
// it authorizes reads and locks the target for a link mutation. The
// mutation itself, its fresh authorization and its audit run through the
// Share Link use case.
type TargetAuthority interface {
	// AuthorizeList requires that the caller may list the links of the
	// target. A missing or hidden target answers not found.
	AuthorizeList(ctx context.Context, entityType EntityType, entityID string) error
	// LockTarget locks the target in tx and returns what its locked
	// lifecycle requires of a link mutation.
	LockTarget(ctx context.Context, tx *gorm.DB, entityType EntityType, entityID string) (LockedTarget, error)
}

// LockedTarget is a target locked for a link mutation.
type LockedTarget struct {
	// Name is the resource name a masked denial answers not found for.
	Name string
	// Can is the action the locked lifecycle requires (manage, or edit
	// archived).
	Can policyv1.Can
	// Lifecycle rejects a target whose lifecycle forbids links. It is
	// answered after authorization so it reveals nothing to a stranger.
	Lifecycle error
	// Audit names the target's record of a link change.
	Audit func(linkID string, operation sharedtelemetry.AuditItemOperation) mutation.Audit
}

// Deps are the collaborators of the Share Link use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	Targets TargetAuthority
	Audit   domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Targets": d.Targets == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("share link dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Share Link management use cases.
type Service struct {
	db        *gorm.DB
	targets   TargetAuthority
	mutations *mutation.Runner
}

// NewService validates deps and returns the Share Link use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, targets: deps.Targets, mutations: mutations}, nil
}

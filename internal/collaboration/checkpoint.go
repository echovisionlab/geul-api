package collaboration

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"gorm.io/gorm"
)

type ContributorResolver interface {
	ResolveActiveSubjects(
		context.Context,
		*gorm.DB,
		[]string,
	) (map[string]auth.AccountIdentitySubject, error)
}

type CheckpointFence struct {
	registry     *Registry
	contributors ContributorResolver
}

func NewCheckpointFence(registry *Registry, contributors ContributorResolver) *CheckpointFence {
	if registry == nil || contributors == nil {
		panic("collaboration checkpoint fence requires registry and contributor resolver")
	}
	return &CheckpointFence{registry: registry, contributors: contributors}
}

func (f *CheckpointFence) RequireCurrentContributors(
	ctx context.Context,
	tx *gorm.DB,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	contributors []string,
) error {
	return f.RequireCurrentContributorsForPermission(
		ctx, tx, resourceType, resourceID, contributors,
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
	)
}

func (f *CheckpointFence) RequireCurrentContributorsForPermission(
	ctx context.Context,
	tx *gorm.DB,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	contributors []string,
	permission intrav1.CollaborationPermission,
) error {
	if len(contributors) == 0 {
		return errs.InvalidArgument(
			"contributor_member_ids",
			"collaboration mutation requires contributors",
		)
	}
	for index, contributor := range contributors {
		contributor = strings.TrimSpace(contributor)
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument(
				"contributor_member_ids",
				"must contain canonical Member UUIDs",
			)
		}
		if index > 0 && contributors[index-1] >= contributor {
			return errs.InvalidArgument(
				"contributor_member_ids",
				"collaboration mutation requires sorted unique Member UUIDs",
			)
		}
	}

	subjects, err := f.contributors.ResolveActiveSubjects(ctx, tx, contributors)
	if err != nil {
		return err
	}
	if len(subjects) != len(contributors) {
		return errs.PermissionDenied("a collaboration contributor is no longer active")
	}
	for _, memberID := range contributors {
		subject, ok := subjects[memberID]
		if !ok {
			return errs.PermissionDenied("a collaboration contributor is no longer active")
		}
		if err := f.registry.RequireForSubjectInTx(
			ctx,
			tx,
			resourceType,
			resourceID,
			permission,
			subject,
		); err != nil {
			return err
		}
	}
	return nil
}

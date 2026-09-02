package emailauthoring

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

func emailTemplateEditCan(templateID string) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(templateID, "email template id"); err != nil {
		return policyv1.Can{}, err
	}
	return policyv1.EmailTemplate.Edit(templateID)
}

func requireEmailTemplateCollaborationContributors(
	ctx context.Context,
	tx *gorm.DB,
	checkpoints persistencecheckpoint.ContributorFence,
	templateID string,
	contributors []string,
) error {
	return checkpoints.RequireCurrentContributors(
		ctx,
		tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE,
		templateID,
		contributors,
	)
}

func emailLayoutEditCan(layoutID string) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(layoutID, "email layout id"); err != nil {
		return policyv1.Can{}, err
	}
	return policyv1.EmailLayout.Edit(layoutID)
}

func requireEmailLayoutCollaborationContributors(
	ctx context.Context,
	tx *gorm.DB,
	checkpoints persistencecheckpoint.ContributorFence,
	layoutID string,
	contributors []string,
) error {
	return checkpoints.RequireCurrentContributors(
		ctx,
		tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_LAYOUT,
		layoutID,
		contributors,
	)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func requireEmailAuthoringMutationContributor(
	ctx context.Context,
	tx *gorm.DB,
	contributors []string,
) (string, error) {
	if len(contributors) != 1 {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"Email authoring mutation requires exactly one origin Member",
		)
	}
	contributor := contributors[0]
	if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"Email authoring mutation requires a canonical Member UUID",
		)
	}
	var count int64
	if err := tx.WithContext(ctx).Table("member").Where("id = ?", contributor).Count(&count).Error; err != nil {
		return "", errs.Internal(err)
	}
	if count != 1 {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"origin Member does not exist",
		)
	}
	return contributor, nil
}

package campaign

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollaborationPermissionChecker is the fully consistent authorization read
// required by Campaign collaboration mutations.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

func campaignEditCan(campaignID string) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(campaignID, "campaign id"); err != nil {
		return policyv1.Can{}, err
	}
	return policyv1.Campaign.Edit(campaignID)
}

type TransactionalPublisher interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableTrimmedString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func campaignEmailTestActorID(ctx context.Context) string {
	if user := auth.GetUser(ctx); user != nil && strings.TrimSpace(user.MemberID.String()) != "" {
		return user.MemberID.String()
	}
	return "system"
}

func publishCampaignDurableProtoInTransaction(
	ctx context.Context,
	publisher TransactionalPublisher,
	tx *gorm.DB,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return fmt.Errorf("database transaction is required")
	}
	executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
	if !ok {
		return fmt.Errorf("database transaction does not expose a PGMQ executor")
	}
	return publisher.EnqueueProtobufWithExecutor(ctx, executor, queue, messageID, message)
}

func requireCampaignContributors(
	ctx context.Context,
	tx *gorm.DB,
	contributors []string,
) error {
	if len(contributors) == 0 {
		return errs.InvalidArgument(
			"contributor_member_ids",
			"collaboration mutation requires contributors",
		)
	}
	for index, contributor := range contributors {
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument(
				"contributor_member_ids",
				"collaboration mutation requires canonical Member UUIDs",
			)
		}
		if index > 0 && contributors[index-1] >= contributor {
			return errs.InvalidArgument(
				"contributor_member_ids",
				"collaboration mutation requires sorted unique Member UUIDs",
			)
		}
	}

	var members []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", contributors).
		Find(&members).Error; err != nil {
		return errs.Internal(fmt.Errorf("lock Campaign contributor Members: %w", err))
	}
	if len(members) != len(contributors) {
		return errs.InvalidArgument(
			"contributor_member_ids",
			"contains a Member that does not exist",
		)
	}
	return nil
}

func requireCampaignCollaborationContributors(
	ctx context.Context,
	tx *gorm.DB,
	checkpoints persistencecheckpoint.ContributorFence,
	campaignID string,
	contributors []string,
) error {
	return checkpoints.RequireCurrentContributors(
		ctx,
		tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN,
		campaignID,
		contributors,
	)
}

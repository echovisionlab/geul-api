package campaign

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// CollaborationPermissionChecker is the fully consistent authorization read
// required by Campaign collaboration mutations.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
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
	executor, err := contentsignal.TransactionExecutor(tx)
	if err != nil {
		return err
	}
	return publisher.EnqueueProtobufWithExecutor(ctx, executor, queue, messageID, message)
}

func requireDependencies(name string, absent map[string]bool) error {
	var missing []string
	for field, isAbsent := range absent {
		if isAbsent {
			missing = append(missing, field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return errors.New(name + " dependencies are required: " + strings.Join(missing, ", "))
}

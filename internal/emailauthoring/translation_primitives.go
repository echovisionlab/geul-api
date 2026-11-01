package emailauthoring

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func emailTemplateEditCan(templateID string) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(templateID, "email template id"); err != nil {
		return policyv1.Can{}, err
	}
	return policyv1.EmailTemplate.Edit(templateID)
}

func emailLayoutEditCan(layoutID string) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(layoutID, "email layout id"); err != nil {
		return policyv1.Can{}, err
	}
	return policyv1.EmailLayout.Edit(layoutID)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// requireOriginMember locks the single Member a collaboration write credits
// and returns it.
func (s *InternalService) requireOriginMember(ctx context.Context, tx *gorm.DB, contributors []string) (string, error) {
	if len(contributors) != 1 {
		return "", domainerr.InvalidField("contributor_member_ids", "Email authoring mutation requires exactly one origin Member")
	}
	contributor := contributors[0]
	if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
		return "", domainerr.InvalidField("contributor_member_ids", "Email authoring mutation requires a canonical Member UUID")
	}
	locked, err := s.members.LockMembers(ctx, tx, contributors)
	if err != nil {
		return "", fmt.Errorf("lock Email authoring origin Member: %w", err)
	}
	if len(locked) != 1 {
		return "", domainerr.InvalidField("contributor_member_ids", "origin Member does not exist")
	}
	return contributor, nil
}

// lockAIDocumentContributor locks the editing Member an AI document write
// credits.
func (s *InternalService) lockAIDocumentContributor(ctx context.Context, tx *gorm.DB, memberID uuid.UUID) error {
	if memberID == uuid.Nil {
		return domainerr.InvalidField("contributor_member_id", "is required")
	}
	locked, err := s.members.LockMembers(ctx, tx, []string{memberID.String()})
	if err != nil {
		return fmt.Errorf("lock Email authoring AI document contributor Member: %w", err)
	}
	if len(locked) != 1 {
		return domainerr.InvalidField("contributor_member_id", "does not identify a current Member")
	}
	return nil
}

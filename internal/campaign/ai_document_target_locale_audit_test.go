package campaign_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCampaignAIDocumentLocaleAuditIsMemberOwnedAndMinimal(t *testing.T) {
	t.Parallel()
	s, units := campaigntest.New()
	writer := &audittest.Recorder{}
	runner, err := mutation.NewStoreRunner[campaign.Store](units, authztest.AllowAll(), &identitystatetest.Fence{}, writer)
	require.NoError(t, err)
	memberID := uuid.NewString()
	record := campaign.LocaleContentChange(memberID, "campaign-1", "ja", sharedtelemetry.AuditItemOperationUpdated)
	_, err = mutation.Run(t.Context(), runner, mutation.StoreSpec[struct{}, campaign.Store]{Access: emailcontent.CollaborationAccess, Change: func(context.Context, campaign.Store) (struct{}, error) { return struct{}{}, nil }, Audit: func(struct{}) mutation.Audit { return record }})
	require.NoError(t, err)
	_ = s
	records := writer.Records()
	require.Len(t, records, 1)
	r := records[0]
	require.NoError(t, r.Validate())
	require.Equal(t, sharedtelemetry.AuditCampaignUpdated, r.Action)
	require.Equal(t, "campaign", r.TargetType)
	require.Equal(t, memberID, r.MemberID)
	require.Equal(t, sharedtelemetry.ActorKindMember, r.Kind)
	require.Equal(t, []string{"locale_content"}, r.ChangedFields)
	require.Equal(t, "ja", r.Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, r.ItemOperation)
	require.Empty(t, r.ContributorMemberIDs)
}
func TestCampaignLocaleAuditFailureRollsBackOwningTransaction(t *testing.T) {
	s, units := campaigntest.New()
	want := errors.New("append failed")
	runner, err := mutation.NewStoreRunner[campaign.Store](units, authztest.AllowAll(), &identitystatetest.Fence{}, &audittest.Recorder{Err: want})
	require.NoError(t, err)
	row := s.PutCampaign(campaign.Campaign{})
	name := "changed"
	_, err = mutation.Run(t.Context(), runner, mutation.StoreSpec[struct{}, campaign.Store]{Access: emailcontent.CollaborationAccess, Change: func(ctx context.Context, store campaign.Store) (struct{}, error) {
		return struct{}{}, store.UpdateCampaign(ctx, row, campaign.CampaignChange{Name: &name})
	}, Audit: func(struct{}) mutation.Audit {
		return campaign.LocaleContentChange(uuid.NewString(), row.ID, "ja", sharedtelemetry.AuditItemOperationCreated)
	}})
	require.ErrorIs(t, err, want)
	stored, _ := s.Campaign(row.ID)
	require.Equal(t, row.Name, stored.Name)
	require.Equal(t, mutation.StepRollback, units.Steps()[len(units.Steps())-1])
}
func TestCampaignLocaleAuditOperationDistinguishesSourceAndTargetCRUD(t *testing.T) {
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailcontent.LocaleContentOperation(true, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailcontent.LocaleContentOperation(false, true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, emailcontent.LocaleContentOperation(false, false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, emailcontent.LocaleContentOperation(false, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, emailcontent.LocaleContentOperation(false, false, true, true))
}

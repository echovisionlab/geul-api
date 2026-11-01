package campaign

import (
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateCampaignRecipientScopeContentRejectsUnsubscribeForAllMembers(t *testing.T) {
	layouts := []CampaignDeliverySnapshotLayout{{Locale: "en", HTMLContent: `<a href="{{ unsubscribe_link }}">Unsubscribe</a>{{content}}`}}
	inLayout := CampaignDeliverySnapshot{Subject: "News", ContentHTML: "<p>Hello</p>", LayoutTranslations: &layouts}
	inTranslation := CampaignDeliverySnapshot{
		Subject: "News", ContentHTML: "<p>Hello</p>",
		Translations: []CampaignDeliverySnapshotTranslation{{Locale: "ko", ContentHTML: `<a href="{{UNSUBSCRIBE_LINK}}">수신 거부</a>`}},
	}
	plain := CampaignDeliverySnapshot{Subject: "Notice", ContentHTML: "<p>{{recipient_email}}</p>"}

	for _, snapshot := range []CampaignDeliverySnapshot{inLayout, inTranslation} {
		err := validateCampaignRecipientScopeContent(campaignRecipientScopeAllMatchingUsers, snapshot)
		require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
		require.NoError(t, validateCampaignRecipientScopeContent(campaignRecipientScopeSubscribedUsers, snapshot))
	}
	require.NoError(t, validateCampaignRecipientScopeContent(campaignRecipientScopeAllMatchingUsers, plain))
}

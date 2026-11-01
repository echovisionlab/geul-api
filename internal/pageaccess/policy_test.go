package pageaccess_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestNormalizeCanonicalizesAndRejectsUnsupportedPolicies(t *testing.T) {
	tag := uuid.NewString()
	normalized, err := pageaccess.Normalize(&pageaccess.Policy{
		Mode:         pageaccess.ModeConditions,
		AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_ADMIN, policyv1.AuthorizationRole_AUTHOR, policyv1.AuthorizationRole_ADMIN},
		UserTagIDs:   []string{tag, tag},
	})
	require.NoError(t, err)
	require.Equal(t, pageaccess.Policy{
		Mode: pageaccess.ModeConditions, Match: pageaccess.MatchAny,
		AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR, policyv1.AuthorizationRole_ADMIN},
		UserTagIDs:   []string{tag},
	}, normalized)
	public, err := pageaccess.Normalize(nil)
	require.NoError(t, err)
	require.Equal(t, pageaccess.Policy{Mode: pageaccess.ModePublic, Match: pageaccess.MatchAny}, public)

	for name, policy := range map[string]pageaccess.Policy{
		"unknown mode":     {Mode: 999},
		"unknown match":    {Mode: pageaccess.ModeConditions, Match: 9, NewsletterSubscriber: true},
		"user role":        {Mode: pageaccess.ModeConditions, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_USER}},
		"no conditions":    {Mode: pageaccess.ModeConditions},
		"malformed tag ID": {Mode: pageaccess.ModeConditions, UserTagIDs: []string{"not-a-uuid"}},
		"nil tag ID":       {Mode: pageaccess.ModeConditions, UserTagIDs: []string{uuid.Nil.String()}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pageaccess.Normalize(&policy)
			require.Error(t, err)
		})
	}
}

func TestEncodeAndDecodeRoundTripTheStoredMessage(t *testing.T) {
	message := &commonv1.PageAccessPolicy{
		Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL,
		AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR}, NewsletterSubscriber: true,
	}
	raw, err := pageaccess.Encode(pageaccess.FromMessage(message))
	require.NoError(t, err)
	decoded, err := pageaccess.Decode(raw)
	require.NoError(t, err)
	require.True(t, proto.Equal(message, pageaccess.Message(decoded)))
	require.True(t, pageaccess.Equal(pageaccess.FromMessage(message), decoded))

	legacy, err := pageaccess.Decode(json.RawMessage("  "))
	require.NoError(t, err)
	require.Equal(t, pageaccess.ModePublic, legacy.Mode)
	_, err = pageaccess.Decode(json.RawMessage(`{"mode":`))
	require.Error(t, err)
}

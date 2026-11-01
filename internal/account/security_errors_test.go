package account_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// lookupFailure answers every base Identity read with err and no Identity.
type lookupFailure struct {
	*accounttest.Kratos
	err error
}

func (m lookupFailure) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return nil, m.err
}

func TestRequestEmailChangePreservesIdentityLookupFailures(t *testing.T) {
	t.Parallel()
	unavailable := errors.New("identity service unavailable")
	for _, tt := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"missing", nil, connect.CodeNotFound},
		{"HTTP not found", &auth.KratosHTTPError{StatusCode: http.StatusNotFound}, connect.CodeNotFound},
		{"HTTP unavailable", &auth.KratosHTTPError{StatusCode: http.StatusServiceUnavailable, Body: "private-upstream-response session_token=private-token"}, connect.CodeInternal},
		{"connection failure", unavailable, connect.CodeInternal},
		{"deadline", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, authztest.AllowAll())
			service, err := account.NewService(account.Deps{
				Units: f.units, SpiceDB: f.authorizer, Fence: f.fence, Identity: lookupFailure{Kratos: f.kratos, err: tt.err},
				Emails: f.emails, Members: f.members, Audit: f.audit,
			})
			require.NoError(t, err)
			handler := account.NewConnectHandler(service)
			ctx := principalContext(t, "22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111")
			response, err := handler.RequestEmailChange(ctx, connect.NewRequest(&managev1.RequestEmailChangeRequest{NewEmail: "next@example.test"}))
			require.Nil(t, response)
			require.Equal(t, tt.code, connect.CodeOf(err))
			if tt.err != nil && tt.code == connect.CodeInternal {
				require.ErrorIs(t, err, tt.err)
				var rpcError *connect.Error
				require.ErrorAs(t, err, &rpcError)
				require.Equal(t, "identity lookup failed", rpcError.Message())
				require.NotContains(t, err.Error(), "private-upstream-response")
				require.NotContains(t, err.Error(), "private-token")
			}
		})
	}
}

func TestDeliveryPrimaryEmailForIdentityReadsExactActiveBilateralMember(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("selected@example.test")

	email, err := f.service.DeliveryPrimaryEmailForIdentity(t.Context(), memberID, identityID)
	require.NoError(t, err)
	require.Equal(t, "selected@example.test", email)

	_, err = f.service.DeliveryPrimaryEmailForIdentity(t.Context(), memberID, uuid.NewString())
	require.Error(t, err)
	f.store.UpdateMember(memberID, func(member *accounttest.Member) { member.Deleted = true })
	_, err = f.service.DeliveryPrimaryEmailForIdentity(t.Context(), memberID, identityID)
	require.Error(t, err)
}

func TestSecurityForIdentityRejectsNonCanonicalMemberPrimaryProjection(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	canonicalEmail := "canonical@example.test"
	selectedEmail := "selected@example.test"
	memberID, identityID := f.seedPair(selectedEmail)
	f.kratos.Update(identityID, func(identity *auth.Identity) {
		identity.Traits = structured.Fields{"email": canonicalEmail}
		identity.VerifiableAddresses = []auth.VerifiableAddress{
			{Via: "email", Value: canonicalEmail, Verified: true},
			{Via: "email", Value: selectedEmail, Verified: true},
		}
	})

	_, err := f.service.GetMySecurity(principalContext(t, memberID, identityID))
	require.ErrorContains(t, err, "primary email is not synchronized")
}

func TestRequestDeletionPreservesLinkResolutionFailure(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		name := "database failure"
		if missing {
			name = "missing active link"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, authztest.AllowAll())
			memberID, identityID := f.seedPair("")
			lookupErr := errors.New("active link database unavailable")
			want := connect.CodeInternal
			if missing {
				f.store.UpdateMember(memberID, func(member *accounttest.Member) { member.Unlinked = true })
				want = connect.CodeFailedPrecondition
			} else {
				f.store.Fail("ValidateActivePair", lookupErr)
			}
			_, err := f.service.RequestAccountDeletion(principalContext(t, memberID, identityID))
			require.Equal(t, want, errs.Wrap(err).Code())
			if !missing {
				require.ErrorIs(t, err, lookupErr)
			}
			require.Empty(t, f.store.DeletionRequests())
			require.Empty(t, f.emails.Sent())
		})
	}
}

func TestRequestDeletionPreservesRecipientAndSnapshotFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"recipient", "snapshot", "missing snapshot"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, authztest.AllowAll())
			memberID, identityID := f.seedPair("member@example.test")
			lookupErr := errors.New(stage + " database unavailable")
			want := connect.CodeInternal
			switch stage {
			case "recipient":
				f.store.Fail("MemberPrimaryEmail", lookupErr)
			case "snapshot":
				f.store.Fail("MemberDeletionNotification", lookupErr)
			case "missing snapshot":
				f.store.Fail("MemberDeletionNotification", account.ErrMemberLinkNotFound)
				want = connect.CodeFailedPrecondition
			}
			_, err := f.service.RequestAccountDeletion(principalContext(t, memberID, identityID))
			require.Equal(t, want, errs.Wrap(err).Code())
			if want == connect.CodeInternal {
				require.ErrorIs(t, err, lookupErr)
			}
			require.Empty(t, f.store.DeletionRequests())
			require.Empty(t, f.emails.Sent())
		})
	}
}

func TestRequestDeletionDoesNotExposeRecipientIdentityFailureBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("member@example.test")
	lookupErr := &auth.KratosHTTPError{StatusCode: http.StatusServiceUnavailable, Body: "private-upstream-response session_token=private-token"}
	calls := 0
	f.kratos.OnCall("GetIdentityWithIncludeCredential", func() {
		calls++
		if calls > 2 {
			f.kratos.Fail("GetIdentityWithIncludeCredential", lookupErr)
		}
	})

	_, err := f.service.RequestAccountDeletion(principalContext(t, memberID, identityID))
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.ErrorIs(t, err, lookupErr)
	require.NotContains(t, err.Error(), "private-upstream-response")
	require.NotContains(t, err.Error(), "private-token")
}

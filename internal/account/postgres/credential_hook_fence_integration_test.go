//go:build integration

package accountpostgres_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestCredentialCompletionReadsCanonicalEmailAfterTheIdentityFenceIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	memberID, identityID, firstEmail := seedPair(t, db, true)
	secondEmail := "next-" + firstEmail
	units, err := accountpostgres.NewEmailProjectionUnits(db, accountadapter.MemberEmailProjection{})
	require.NoError(t, err)
	fakeStore, _ := accounttest.New()
	identity := accounttest.NewKratos(fakeStore)
	code := auth.Credential{Type: "code", Identifiers: []string{firstEmail, secondEmail}}
	identity.Put(&auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": firstEmail}, Credentials: map[string]auth.Credential{"code": code},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Value: firstEmail, Via: "email", Verified: true}, {Value: secondEmail, Via: "email", Verified: true},
		},
	})
	var reads atomic.Int32
	identity.OnCall("GetIdentity", func() { reads.Add(1) })
	identity.OnCall("GetIdentityWithIncludeCredential", func() { reads.Add(1) })
	lifecycle := account.NewAccountCredentialHookLifecycle(units, identity, apitelemetry.NewDurableWriter(db), &accounttest.Emails{})
	input := account.AccountCredentialHookInput{
		AuditID: uuid.NewString(), FlowID: "credential-fence-flow", IdentityID: identityID, Kind: account.AccountCredentialOIDC,
		Credentials: map[string]auth.Credential{"code": code},
		PreviousCredentials: map[string]auth.Credential{
			"code": code,
			"oidc": {Type: "oidc", Identifiers: []string{"google:subject"}, Config: structured.Fields{
				"providers": structured.Values{structured.Fields{"provider": "google", "subject": "subject"}},
			}},
		},
		PreviousSnapshotPresent: true, CredentialSnapshotPresent: true,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseFence := func() { releaseOnce.Do(func() { close(release) }) }
	holderDone := make(chan error, 1)
	completionDone := make(chan error, 1)
	holderJoined, completionJoined := false, false
	t.Cleanup(func() {
		releaseFence()
		cancel()
		if !holderJoined {
			<-holderDone
		}
		if !completionJoined {
			<-completionDone
		}
	})
	go func() {
		holderDone <- units.Fenced(ctx, identityID, func(ctx context.Context, steps account.Steps) error {
			close(held)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
			identity.Update(identityID, func(value *auth.Identity) { value.Traits["email"] = secondEmail })
			return steps.Step(ctx, func(store account.Store) error {
				return store.SyncMemberEmails(ctx, memberID, identityID, secondEmail, []string{firstEmail, secondEmail})
			})
		})
	}()
	select {
	case <-held:
	case err := <-holderDone:
		holderJoined = true
		completionJoined = true
		require.NoError(t, err)
		t.Fatal("identity fence holder returned before entering")
	}
	go func() { completionDone <- lifecycle.Complete(ctx, input) }()
	pgtest.RequireLockWaiters(t, db, 1)
	require.Zero(t, reads.Load(), "completion must wait before its first committed identity read")
	releaseFence()
	err = <-holderDone
	holderJoined = true
	require.NoError(t, err)
	err = <-completionDone
	completionJoined = true
	require.NoError(t, err)
	var primaryEmail string
	require.NoError(t, db.Raw("SELECT primary_email FROM member WHERE id = ?::uuid", memberID).Scan(&primaryEmail).Error)
	require.Equal(t, secondEmail, primaryEmail)
	require.Equal(t, secondEmail, identity.Identity(identityID).CurrentEmail())
	var audits int64
	require.NoError(t, db.Table("public.domain_audit").Where("audit_id = ?::uuid", input.AuditID).Count(&audits).Error)
	require.EqualValues(t, 1, audits)
}

//go:build integration

package page

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPolicyAuthorityLocksPageRootAndFencesCurrentPrincipalIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Page policy admin")
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})

	pageID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'page')`,
		documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO page (id, content_document_id) VALUES (?::uuid, ?::uuid)`,
		pageID, documentID,
	).Error)
	policy, err := policyv1.Page.TouchPolicy(pageID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)

	authority := NewPolicyAuthority(spiceDB)
	requireLocked := func(invoke func(*gorm.DB) error) error {
		tx := db.Begin()
		require.NoError(t, tx.Error)
		defer func() { require.NoError(t, tx.Rollback().Error) }()
		return invoke(tx)
	}

	missingPageID := uuid.NewString()
	err = requireLocked(func(tx *gorm.DB) error {
		return authority.RequireLockedView(ctx, tx, missingPageID)
	})
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	require.NoError(t, requireLocked(func(tx *gorm.DB) error {
		return authority.RequireLockedView(ctx, tx, pageID)
	}))
	require.NoError(t, requireLocked(func(tx *gorm.DB) error {
		return authority.RequireLockedEdit(ctx, tx, pageID)
	}))

	require.NoError(t, db.Table("member").Where("id = ?", memberID).Update("onboarded", false).Error)
	err = requireLocked(func(tx *gorm.DB) error {
		return authority.RequireLockedEdit(ctx, tx, pageID)
	})
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCollaborationSessionLockFencesRevocationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Page collaboration session")
	sessionID := insertPageIntegrationSession(t, db, identityID)
	locked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseLock()
	authorized := make(chan error, 1)
	go func() {
		authorized <- db.Transaction(func(tx *gorm.DB) error {
			principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(t.Context(), tx, sessionID)
			if err != nil {
				return err
			}
			active, err := identitystate.LockActivePrincipal(t.Context(), tx, principal)
			if err != nil {
				return err
			}
			if !active {
				return errs.InternalMsg("collaboration principal is not active")
			}
			if err := auth.LockAuthenticatedSessionForPrincipal(t.Context(), tx, sessionID, principal); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-authorized:
		require.NoError(t, err)
		t.Fatal("collaboration session lock transaction returned before the test fence")
	case <-time.After(5 * time.Second):
		t.Fatal("collaboration session did not reach the locked fence")
	}

	revoked := make(chan error, 1)
	go func() {
		revoked <- db.Exec(`UPDATE kratos.sessions SET active = FALSE WHERE id = ?::uuid`, sessionID).Error
	}()
	select {
	case err := <-revoked:
		require.NoError(t, err)
		t.Fatal("session revocation committed while collaboration delivery authority held its lock")
	case <-time.After(150 * time.Millisecond):
	}

	releaseLock()
	require.NoError(t, <-authorized)
	select {
	case err := <-revoked:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("session revocation did not commit after collaboration authority released its lock")
	}
}

func TestPageCollaborationSignFirstDoesNotInvertAccountSessionDeletionIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	user := stack.CreateUser(t, policyv1.Role.Admin().ID())
	pageID, documentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, stack.DB.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'page', ?::uuid)`,
		documentID, uuid.NewString(),
	).Error)
	require.NoError(t, stack.DB.Exec(
		`INSERT INTO page (id, content_document_id) VALUES (?::uuid, ?::uuid)`,
		pageID, documentID,
	).Error)
	policy, err := policyv1.Page.TouchPolicy(pageID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)

	authorized := make(chan struct{})
	releaseSigner := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSigner) }) }
	defer release()
	signerDone := make(chan error, 1)
	go func() {
		signerDone <- stack.DB.Transaction(func(tx *gorm.DB) error {
			if _, err := loadPageContentDocumentIDForRead(t.Context(), tx, pageID); err != nil {
				return err
			}
			if _, err := authorizePageBlockBootstrap(
				t.Context(), tx, stack.SpiceDBClient, pageID,
				&intrav1.CollaborationPrincipal{SessionId: user.SessionID},
			); err != nil {
				return err
			}
			close(authorized)
			<-releaseSigner
			return nil
		})
	}()
	select {
	case <-authorized:
	case err := <-signerDone:
		require.NoError(t, err)
		t.Fatal("Page signer returned before reaching the final session fence")
	case <-time.After(5 * time.Second):
		t.Fatal("Page signer did not reach the final session fence")
	}

	identityMutationAcquired := make(chan struct{})
	deletionDone := make(chan error, 1)
	go func() {
		deletionDone <- identitystate.WithMutation(t.Context(), stack.DB, user.IdentityID, func(ctx context.Context, _ *gorm.DB) error {
			close(identityMutationAcquired)
			return stack.KratosClient.DeleteIdentitySessions(ctx, user.IdentityID)
		})
	}()
	select {
	case <-identityMutationAcquired:
		t.Fatal("account session deletion acquired the Identity fence while signing held it")
	case err := <-deletionDone:
		require.NoError(t, err)
		t.Fatal("account session deletion completed before signing released its authority fence")
	case <-time.After(150 * time.Millisecond):
	}

	release()
	require.NoError(t, <-signerDone)
	select {
	case <-identityMutationAcquired:
	case err := <-deletionDone:
		require.NoError(t, err)
		t.Fatal("account session deletion completed without entering its Identity mutation")
	case <-time.After(5 * time.Second):
		t.Fatal("account session deletion did not acquire the Identity fence after signing completed")
	}
	select {
	case err := <-deletionDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("account session deletion stalled after signing completed")
	}
}

func TestPageCollaborationAccountSessionDeletionFirstDoesNotWaitOnSignerIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	user := stack.CreateUser(t, policyv1.Role.Admin().ID())
	pageID, documentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, stack.DB.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'page', ?::uuid)`,
		documentID, uuid.NewString(),
	).Error)
	require.NoError(t, stack.DB.Exec(
		`INSERT INTO page (id, content_document_id) VALUES (?::uuid, ?::uuid)`,
		pageID, documentID,
	).Error)

	identityMutationAcquired := make(chan struct{})
	releaseDeletion := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDeletion) }) }
	defer release()
	deletionDone := make(chan error, 1)
	go func() {
		deletionDone <- identitystate.WithMutation(t.Context(), stack.DB, user.IdentityID, func(ctx context.Context, _ *gorm.DB) error {
			close(identityMutationAcquired)
			<-releaseDeletion
			return stack.KratosClient.DeleteIdentitySessions(ctx, user.IdentityID)
		})
	}()
	select {
	case <-identityMutationAcquired:
	case err := <-deletionDone:
		require.NoError(t, err)
		t.Fatal("account session deletion returned before its deterministic barrier")
	case <-time.After(5 * time.Second):
		t.Fatal("account session deletion did not acquire the Identity fence")
	}

	phaseOneResolved := make(chan struct{})
	signerDone := make(chan error, 1)
	go func() {
		signerDone <- stack.DB.Transaction(func(tx *gorm.DB) error {
			if _, err := loadPageContentDocumentIDForRead(t.Context(), tx, pageID); err != nil {
				return err
			}
			principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(t.Context(), tx, user.SessionID)
			if err != nil {
				return err
			}
			close(phaseOneResolved)
			active, err := identitystate.LockActivePrincipal(t.Context(), tx, principal)
			if err != nil {
				return err
			}
			if !active {
				return errs.NoPermission("edit", "page")
			}
			return auth.LockAuthenticatedSessionForPrincipal(t.Context(), tx, user.SessionID, principal)
		})
	}()
	select {
	case <-phaseOneResolved:
	case err := <-signerDone:
		require.NoError(t, err)
		t.Fatal("Page signer returned before reaching the Identity fence")
	case <-time.After(5 * time.Second):
		t.Fatal("Page signer did not complete nonlocking session phase one")
	}
	select {
	case err := <-signerDone:
		require.NoError(t, err)
		t.Fatal("Page signer passed the Identity fence while account deletion held it")
	case <-time.After(150 * time.Millisecond):
	}

	release()
	select {
	case err := <-deletionDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("account session deletion waited on the phase-one Page signer")
	}
	select {
	case err := <-signerDone:
		require.ErrorIs(t, err, auth.ErrSessionPrincipalInvalid)
	case <-time.After(5 * time.Second):
		t.Fatal("Page signer did not deny the deleted session after account mutation completed")
	}
}

func TestCollaborationSessionRevocationFirstDeniesFinalFenceIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Page revoked collaboration session")
	sessionID := insertPageIntegrationSession(t, db, identityID)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { require.NoError(t, tx.Rollback().Error) }()
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(t.Context(), tx, sessionID)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE kratos.sessions SET active = FALSE WHERE id = ?::uuid`, sessionID).Error)
	active, err := identitystate.LockActivePrincipal(t.Context(), tx, principal)
	require.NoError(t, err)
	require.True(t, active)

	err = auth.LockAuthenticatedSessionForPrincipal(t.Context(), tx, sessionID, principal)

	require.ErrorIs(t, err, auth.ErrSessionPrincipalInvalid)
}

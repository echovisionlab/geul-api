package sharelinkadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type recordedPermissionCheck struct {
	decision policyv1.AuthorizationDecision
}

type recordingPermissionChecker struct {
	allowed bool
	err     error
	calls   []recordedPermissionCheck
}

func (c *recordingPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls = append(c.calls, recordedPermissionCheck{decision: decision})
	return c.allowed, c.err
}

func TestAuthorityChecksOrdinaryShareLinkActionExactlyOnceForEveryTarget(t *testing.T) {
	t.Parallel()

	targets := []struct {
		name           string
		entityType     sharelink.EntityType
		expectedAction auth.ResourceAction
	}{
		{name: "post", entityType: sharelink.EntityPost, expectedAction: policyv1.Post.ManageShareLinks},
		{name: "page", entityType: sharelink.EntityPage, expectedAction: policyv1.Page.ManageShareLinks},
		{name: "work", entityType: sharelink.EntityWork, expectedAction: policyv1.Work.ManageShareLinks},
		{name: "release", entityType: sharelink.EntityRelease, expectedAction: policyv1.Release.ManageShareLinks},
		{name: "form", entityType: sharelink.EntityForm, expectedAction: policyv1.Form.ManageShareLinks},
		{name: "form dashboard", entityType: sharelink.EntityFormDashboard, expectedAction: policyv1.Form.ManageShareLinks},
		{name: "artist", entityType: sharelink.EntityArtist, expectedAction: policyv1.Artist.ManageShareLinks},
		{name: "label", entityType: sharelink.EntityLabel, expectedAction: policyv1.Label.ManageShareLinks},
		{name: "privacy", entityType: sharelink.EntityPrivacy, expectedAction: policyv1.PrivacyHistory.ManageShareLinks},
		{name: "terms", entityType: sharelink.EntityTerms, expectedAction: policyv1.TermsHistory.ManageShareLinks},
	}
	operations := []authorityTestOperation{authorityOperationList, authorityOperationCreate, authorityOperationDelete}

	for _, target := range targets {
		target := target
		for _, operation := range operations {
			operation := operation
			t.Run(target.name+"/"+string(operation), func(t *testing.T) {
				t.Parallel()

				db := newAuthorityUnitDB(t)
				entityID := uuid.NewString()
				seedAuthorityTarget(t, db, target.entityType, entityID, false)
				checker := &recordingPermissionChecker{allowed: true}
				authority := newAuthority(db, checker)

				locked, err := executeAuthorityOperation(t, authority, target.entityType, entityID, operation)
				require.NoError(t, err)
				requireOperationAction(t, checker, locked, operation, target.expectedAction, entityID)
				if operation != authorityOperationList {
					require.NoError(t, locked.Lifecycle)
				}
			})
		}
	}
}

func TestAuthoritySelectsArchivedReadAndMutationPermissionsExactlyOnce(t *testing.T) {
	t.Parallel()

	targets := []struct {
		name         string
		entityType   sharelink.EntityType
		viewArchived auth.ResourceAction
		editArchived auth.ResourceAction
		legal        bool
	}{
		{name: "post", entityType: sharelink.EntityPost, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived},
		{name: "work", entityType: sharelink.EntityWork, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived},
		{name: "privacy", entityType: sharelink.EntityPrivacy, viewArchived: policyv1.PrivacyHistory.ViewArchived, editArchived: policyv1.PrivacyHistory.EditArchived, legal: true},
		{name: "terms", entityType: sharelink.EntityTerms, viewArchived: policyv1.TermsHistory.ViewArchived, editArchived: policyv1.TermsHistory.EditArchived, legal: true},
	}
	operations := []authorityTestOperation{authorityOperationList, authorityOperationCreate, authorityOperationDelete}

	for _, target := range targets {
		target := target
		for _, operation := range operations {
			operation := operation
			t.Run(target.name+"/"+string(operation), func(t *testing.T) {
				t.Parallel()

				db := newAuthorityUnitDB(t)
				entityID := uuid.NewString()
				seedAuthorityTarget(t, db, target.entityType, entityID, true)
				checker := &recordingPermissionChecker{allowed: true}
				authority := newAuthority(db, checker)

				locked, err := executeAuthorityOperation(t, authority, target.entityType, entityID, operation)
				expectedAction := target.editArchived
				if operation == authorityOperationList {
					expectedAction = target.viewArchived
				}
				if target.legal && operation == authorityOperationList {
					requirePermissionCheck(t, checker, expectedAction, entityID)
					require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
					return
				}
				require.NoError(t, err)
				requireOperationAction(t, checker, locked, operation, expectedAction, entityID)
				if target.legal {
					require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(locked.Lifecycle))
				}
			})
		}
	}
}

func TestAuthorityMasksMissingAndDeniedListTargets(t *testing.T) {
	t.Parallel()

	for _, existing := range []bool{false, true} {
		existing := existing
		name := "missing"
		if existing {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := newAuthorityUnitDB(t)
			entityID := uuid.NewString()
			if existing {
				seedAuthorityTarget(t, db, sharelink.EntityPage, entityID, false)
			}
			checker := &recordingPermissionChecker{}
			authority := newAuthority(db, checker)

			_, err := executeAuthorityOperation(t, authority, sharelink.EntityPage, entityID, authorityOperationList)
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			if !existing {
				require.Empty(t, checker.calls)
				return
			}
			requirePermissionCheck(t, checker, policyv1.Page.ManageShareLinks, entityID)
		})
	}
}

func TestAuthorityRejectsLockingAMissingTarget(t *testing.T) {
	t.Parallel()

	checker := &recordingPermissionChecker{}
	_, err := executeAuthorityOperation(t, newAuthority(newAuthorityUnitDB(t), checker), sharelink.EntityPage, uuid.NewString(), authorityOperationCreate)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Empty(t, checker.calls)
}

// The use case checks the selected action before the lifecycle, so the
// legal preview invariant reveals nothing to a caller without it.
func TestAuthorityReportsTheLegalPreviewInvariantSeparately(t *testing.T) {
	t.Parallel()

	db := newAuthorityUnitDB(t)
	entityID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO privacy_history (id, status, effective_from, version) VALUES (?, ?, ?, 7)`,
		entityID,
		managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
		time.Now().UTC().Add(time.Hour),
	).Error)
	checker := &recordingPermissionChecker{}

	locked, err := executeAuthorityOperation(t, newAuthority(db, checker), sharelink.EntityPrivacy, entityID, authorityOperationCreate)
	require.NoError(t, err)
	requireOperationAction(t, checker, locked, authorityOperationCreate, policyv1.PrivacyHistory.ManageShareLinks, entityID)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(locked.Lifecycle))
}

func TestAuthorityMapsSpiceDBFailureOfAList(t *testing.T) {
	t.Parallel()

	db := newAuthorityUnitDB(t)
	entityID := uuid.NewString()
	seedAuthorityTarget(t, db, sharelink.EntityLabel, entityID, false)
	checker := &recordingPermissionChecker{err: errors.New("spicedb unavailable")}
	authority := newAuthority(db, checker)

	_, err := executeAuthorityOperation(t, authority, sharelink.EntityLabel, entityID, authorityOperationList)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	requirePermissionCheck(t, checker, policyv1.Label.ManageShareLinks, entityID)
}

type authorityTestOperation string

const (
	authorityOperationList   authorityTestOperation = "list"
	authorityOperationCreate authorityTestOperation = "create"
	authorityOperationDelete authorityTestOperation = "delete"
)

func executeAuthorityOperation(
	t *testing.T,
	authority *Authority,
	entityType sharelink.EntityType,
	entityID string,
	operation authorityTestOperation,
) (sharelink.LockedTarget, error) {
	t.Helper()
	switch operation {
	case authorityOperationList:
		return sharelink.LockedTarget{}, authority.AuthorizeList(authorityTestContext(), entityType, entityID)
	case authorityOperationCreate, authorityOperationDelete:
		var locked sharelink.LockedTarget
		err := authority.db.Transaction(func(tx *gorm.DB) error {
			var err error
			locked, err = authority.LockTarget(authorityTestContext(), tx, entityType, entityID)
			return err
		})
		return locked, err
	default:
		t.Fatalf("unsupported authority test operation %q", operation)
		return sharelink.LockedTarget{}, nil
	}
}

// requireOperationAction asserts the one action an operation requires: a
// list asks SpiceDB once, a mutation returns the action for the use case's
// fresh check and asks nothing itself.
func requireOperationAction(
	t *testing.T,
	checker *recordingPermissionChecker,
	locked sharelink.LockedTarget,
	operation authorityTestOperation,
	expectedAction auth.ResourceAction,
	resourceID string,
) {
	t.Helper()
	if operation == authorityOperationList {
		requirePermissionCheck(t, checker, expectedAction, resourceID)
		return
	}
	require.Empty(t, checker.calls)
	expectedCan, err := expectedAction(resourceID)
	require.NoError(t, err)
	require.Equal(t, expectedCan.EngineKey(), locked.Can.EngineKey())
	require.NotNil(t, locked.Audit)
}

func requirePermissionCheck(
	t *testing.T,
	checker *recordingPermissionChecker,
	expectedAction auth.ResourceAction,
	resourceID string,
) {
	t.Helper()
	require.Len(t, checker.calls, 1)
	expectedCan, err := expectedAction(resourceID)
	require.NoError(t, err)
	require.Equal(t, expectedCan.Resource().Type(), checker.calls[0].decision.Resource().Type())
	require.Equal(t, expectedCan.Resource().ID(), checker.calls[0].decision.Resource().ID())
	require.Equal(t, expectedCan.Action().Name(), checker.calls[0].decision.Action().Name())
	require.Equal(t, expectedCan.Action().Permission(), checker.calls[0].decision.Action().Permission())
	require.Equal(t, authorityTestIdentityID.String(), checker.calls[0].decision.Actor().AccountIdentityID())
}

const authorityTestIdentityID = auth.IdentityID("00000000-0000-4000-8000-000000000101")

func authorityTestContext() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    authorityTestIdentityID,
		MemberID:      auth.MemberID("00000000-0000-4000-8000-000000000102"),
		SessionID:     auth.SessionID("00000000-0000-4000-8000-000000000103"),
		Authenticated: true,
		Onboarded:     true,
	})
}

func newAuthorityUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE post (id TEXT PRIMARY KEY, status TEXT NOT NULL);
		CREATE TABLE page (id TEXT PRIMARY KEY);
		CREATE TABLE work (id TEXT PRIMARY KEY, status TEXT NOT NULL);
		CREATE TABLE release (id TEXT PRIMARY KEY);
		CREATE TABLE form (id TEXT PRIMARY KEY);
		CREATE TABLE label (id TEXT PRIMARY KEY);
		CREATE TABLE artist (id TEXT PRIMARY KEY);
		CREATE TABLE privacy_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME, version INTEGER NOT NULL DEFAULT 1);
		CREATE TABLE terms_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME, version INTEGER NOT NULL DEFAULT 1);
	`).Error)
	return db
}

func seedAuthorityTarget(
	t *testing.T,
	db *gorm.DB,
	entityType sharelink.EntityType,
	entityID string,
	archived bool,
) {
	t.Helper()
	switch entityType {
	case sharelink.EntityPost:
		status := managev1.PostStatus_POST_STATUS_DRAFT.String()
		if archived {
			status = managev1.PostStatus_POST_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(`INSERT INTO post (id, status) VALUES (?, ?)`, entityID, status).Error)
	case sharelink.EntityWork:
		status := managev1.WorkStatus_WORK_STATUS_DRAFT.String()
		if archived {
			status = managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(`INSERT INTO work (id, status) VALUES (?, ?)`, entityID, status).Error)
	case sharelink.EntityPrivacy:
		status := managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String()
		if archived {
			status = managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(
			`INSERT INTO privacy_history (id, status, effective_from) VALUES (?, ?, ?)`,
			entityID, status, time.Now().UTC().Add(time.Hour),
		).Error)
	case sharelink.EntityTerms:
		status := managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String()
		if archived {
			status = managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(
			`INSERT INTO terms_history (id, status, effective_from) VALUES (?, ?, ?)`,
			entityID, status, time.Now().UTC().Add(time.Hour),
		).Error)
	default:
		target, err := targetFor(entityType)
		require.NoError(t, err)
		require.NoError(t, db.Table(target.table).Create(map[string]any{"id": entityID}).Error)
	}
}

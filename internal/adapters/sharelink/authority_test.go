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
	"github.com/echovisionlab/geul-api/internal/model"
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
		entityType     managev1.ShareLinkEntityType
		expectedAction auth.ResourceAction
	}{
		{name: "post", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST, expectedAction: policyv1.Post.ManageShareLinks},
		{name: "page", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, expectedAction: policyv1.Page.ManageShareLinks},
		{name: "work", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, expectedAction: policyv1.Work.ManageShareLinks},
		{name: "form", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, expectedAction: policyv1.Form.ManageShareLinks},
		{name: "form dashboard", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD, expectedAction: policyv1.Form.ManageShareLinks},
		{name: "privacy", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY, expectedAction: policyv1.PrivacyHistory.ManageShareLinks},
		{name: "terms", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS, expectedAction: policyv1.TermsHistory.ManageShareLinks},
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
				authority := newAuthority(db, checker, nil)

				continued, err := executeAuthorityOperation(t, authority, target.entityType, entityID, operation)
				require.NoError(t, err)
				require.Equal(t, operation != authorityOperationList, continued)
				requirePermissionCheck(t, checker, target.expectedAction, entityID)
			})
		}
	}
}

func TestAuthoritySelectsArchivedReadAndMutationPermissionsExactlyOnce(t *testing.T) {
	t.Parallel()

	targets := []struct {
		name         string
		entityType   managev1.ShareLinkEntityType
		viewArchived auth.ResourceAction
		editArchived auth.ResourceAction
		legal        bool
	}{
		{name: "post", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived},
		{name: "work", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived},
		{name: "privacy", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY, viewArchived: policyv1.PrivacyHistory.ViewArchived, editArchived: policyv1.PrivacyHistory.EditArchived, legal: true},
		{name: "terms", entityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS, viewArchived: policyv1.TermsHistory.ViewArchived, editArchived: policyv1.TermsHistory.EditArchived, legal: true},
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
				authority := newAuthority(db, checker, nil)

				continued, err := executeAuthorityOperation(t, authority, target.entityType, entityID, operation)
				expectedAction := target.editArchived
				if operation == authorityOperationList {
					expectedAction = target.viewArchived
				}
				requirePermissionCheck(t, checker, expectedAction, entityID)
				if target.legal {
					require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
					require.False(t, continued)
					return
				}
				require.NoError(t, err)
				require.Equal(t, operation != authorityOperationList, continued)
			})
		}
	}
}

func TestAuthorityMasksMissingAndDeniedPrivateTargets(t *testing.T) {
	t.Parallel()

	operations := []authorityTestOperation{authorityOperationList, authorityOperationCreate, authorityOperationDelete}
	for _, operation := range operations {
		operation := operation
		for _, existing := range []bool{false, true} {
			existing := existing
			name := "missing"
			if existing {
				name = "denied"
			}
			t.Run(name+"/"+string(operation), func(t *testing.T) {
				t.Parallel()

				db := newAuthorityUnitDB(t)
				entityID := uuid.NewString()
				if existing {
					seedAuthorityTarget(t, db, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, entityID, false)
				}
				checker := &recordingPermissionChecker{}
				authority := newAuthority(db, checker, nil)

				continued, err := executeAuthorityOperation(t, authority, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, entityID, operation)
				require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
				require.False(t, continued)
				if !existing {
					require.Empty(t, checker.calls)
					return
				}
				requirePermissionCheck(t, checker, policyv1.Page.ManageShareLinks, entityID)
			})
		}
	}
}

func TestAuthorityChecksPermissionBeforeLegalPreviewInvariant(t *testing.T) {
	t.Parallel()

	for _, allowed := range []bool{false, true} {
		allowed := allowed
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			t.Parallel()

			db := newAuthorityUnitDB(t)
			entityID := uuid.NewString()
			require.NoError(t, db.Exec(
				`INSERT INTO privacy_history (id, status, effective_from) VALUES (?, ?, ?)`,
				entityID,
				managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
				time.Now().UTC().Add(time.Hour),
			).Error)
			checker := &recordingPermissionChecker{allowed: allowed}
			authority := newAuthority(db, checker, nil)

			continued, err := executeAuthorityOperation(
				t, authority, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY, entityID, authorityOperationCreate,
			)
			requirePermissionCheck(t, checker, policyv1.PrivacyHistory.ManageShareLinks, entityID)
			require.False(t, continued)
			if allowed {
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
				return
			}
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		})
	}
}

func TestAuthorityMapsSpiceDBFailureWithoutRunningMutation(t *testing.T) {
	t.Parallel()

	db := newAuthorityUnitDB(t)
	entityID := uuid.NewString()
	seedAuthorityTarget(t, db, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, entityID, false)
	checker := &recordingPermissionChecker{err: errors.New("spicedb unavailable")}
	authority := newAuthority(db, checker, nil)

	continued, err := executeAuthorityOperation(
		t, authority, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK, entityID, authorityOperationDelete,
	)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.False(t, continued)
	requirePermissionCheck(t, checker, policyv1.Work.ManageShareLinks, entityID)
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
	entityType managev1.ShareLinkEntityType,
	entityID string,
	operation authorityTestOperation,
) (bool, error) {
	t.Helper()
	continued := false
	link := model.ShareLink{ID: uuid.NewString(), EntityType: entityType.String(), EntityID: entityID}
	switch operation {
	case authorityOperationList:
		return false, authority.AuthorizeList(authorityTestContext(), entityType, entityID)
	case authorityOperationCreate:
		err := authority.Create(authorityTestContext(), entityType, entityID, &link, func(context.Context, *gorm.DB, *model.ShareLink) error {
			continued = true
			return nil
		})
		return continued, err
	case authorityOperationDelete:
		err := authority.Delete(authorityTestContext(), entityType, link, func(context.Context, *gorm.DB, model.ShareLink) error {
			continued = true
			return nil
		})
		return continued, err
	default:
		t.Fatalf("unsupported authority test operation %q", operation)
		return false, nil
	}
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
		CREATE TABLE form (id TEXT PRIMARY KEY);
		CREATE TABLE privacy_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME);
		CREATE TABLE terms_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME);
	`).Error)
	return db
}

func seedAuthorityTarget(
	t *testing.T,
	db *gorm.DB,
	entityType managev1.ShareLinkEntityType,
	entityID string,
	archived bool,
) {
	t.Helper()
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST:
		status := managev1.PostStatus_POST_STATUS_DRAFT.String()
		if archived {
			status = managev1.PostStatus_POST_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(`INSERT INTO post (id, status) VALUES (?, ?)`, entityID, status).Error)
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK:
		status := managev1.WorkStatus_WORK_STATUS_DRAFT.String()
		if archived {
			status = managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(`INSERT INTO work (id, status) VALUES (?, ?)`, entityID, status).Error)
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY:
		status := managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String()
		if archived {
			status = managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()
		}
		require.NoError(t, db.Exec(
			`INSERT INTO privacy_history (id, status, effective_from) VALUES (?, ?, ?)`,
			entityID, status, time.Now().UTC().Add(time.Hour),
		).Error)
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
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

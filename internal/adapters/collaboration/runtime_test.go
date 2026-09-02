package collaborationadapter

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type permissionCheck struct {
	actor policyv1.Actor
	can   policyv1.Can
}

type recordingPermissionChecker struct {
	allowed bool
	err     error
	calls   []permissionCheck
}

func (c *recordingPermissionChecker) CheckActorCan(
	_ context.Context,
	actor policyv1.Actor,
	can policyv1.Can,
) (bool, error) {
	c.calls = append(c.calls, permissionCheck{actor: actor, can: can})
	return c.allowed, c.err
}

func newAuthorizationUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func TestResourceAuthorizerUsesLifecycleSpecificObjectPermissionExactlyOnce(t *testing.T) {
	t.Parallel()
	resourceID := uuid.NewString()
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}

	for _, testCase := range []struct {
		name      string
		archived  bool
		requested intrav1.CollaborationPermission
	}{
		{"ordinary view", false, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW},
		{"ordinary edit", false, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT},
		{"archived view", true, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW},
		{"archived edit", true, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			checker := &recordingPermissionChecker{allowed: true}
			lifecycleCalls := 0
			authorizer := resourceAuthorizer{
				db: newAuthorizationUnitDB(t), checker: checker,
				can: collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived},
				readRoot: func(context.Context, *gorm.DB, string) (bool, bool, error) {
					lifecycleCalls++
					return testCase.archived, true, nil
				},
			}

			allowed, err := authorizer.Authorize(t.Context(), resourceID, testCase.requested, subject)

			require.NoError(t, err)
			require.True(t, allowed)
			require.Equal(t, 1, lifecycleCalls)
			require.Len(t, checker.calls, 1)
			wantCan, canErr := authorizer.can.forPermission(resourceID, testCase.requested, testCase.archived)
			require.NoError(t, canErr)
			require.Equal(t, wantCan.EngineKey(), checker.calls[0].can.EngineKey())
			require.Equal(t, subject.ID.String(), checker.calls[0].actor.AccountIdentityID())
		})
	}
}

func TestMenuManageAuthorizationUsesOnlyTypedManagePermission(t *testing.T) {
	t.Parallel()
	resourceID := uuid.NewString()
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}
	checker := &recordingPermissionChecker{allowed: true}
	authorizer := resourceAuthorizer{
		db: newAuthorizationUnitDB(t), checker: checker,
		can: collaborationCanSet{
			view: policyv1.Menu.View, edit: policyv1.Menu.Edit, manage: policyv1.Menu.Manage,
		},
		readRoot: func(context.Context, *gorm.DB, string) (bool, bool, error) {
			return false, true, nil
		},
	}

	allowed, err := authorizer.Authorize(
		t.Context(), resourceID,
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE,
		subject,
	)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Len(t, checker.calls, 1)
	want, err := policyv1.Menu.Manage(resourceID)
	require.NoError(t, err)
	require.Equal(t, want.EngineKey(), checker.calls[0].can.EngineKey())
}

func TestResourceAuthorizerReusesCheckpointTransaction(t *testing.T) {
	t.Parallel()
	db := newAuthorizationUnitDB(t)
	checker := &recordingPermissionChecker{allowed: true}
	var received *gorm.DB
	authorizer := resourceAuthorizer{
		db: db, checker: checker,
		can: collaborationCanSet{edit: policyv1.Menu.Edit},
		readRoot: func(_ context.Context, tx *gorm.DB, _ string) (bool, bool, error) {
			received = tx
			return false, true, nil
		},
	}
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })

	allowed, err := authorizer.AuthorizeInTx(
		t.Context(), tx, uuid.NewString(),
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
		auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())},
	)

	require.NoError(t, err)
	require.True(t, allowed)
	require.Same(t, tx, received)
}

func TestResourceAuthorizerHidesMissingLifecycleRootWithoutPermissionFallback(t *testing.T) {
	t.Parallel()
	checker := &recordingPermissionChecker{allowed: true}
	authorizer := resourceAuthorizer{
		db: newAuthorizationUnitDB(t), checker: checker,
		can: collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived},
		readRoot: func(context.Context, *gorm.DB, string) (bool, bool, error) {
			return false, false, nil
		},
	}

	allowed, err := authorizer.Authorize(
		t.Context(), uuid.NewString(), intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
		auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())},
	)

	require.NoError(t, err)
	require.False(t, allowed)
	require.Empty(t, checker.calls)
}

func TestResourceAuthorizerDoesNotFallbackAfterArchivedPermissionDenial(t *testing.T) {
	t.Parallel()
	checker := &recordingPermissionChecker{allowed: false}
	authorizer := resourceAuthorizer{
		db: newAuthorizationUnitDB(t), checker: checker,
		can: collaborationCanSet{view: policyv1.Work.View, edit: policyv1.Work.Edit, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived},
		readRoot: func(context.Context, *gorm.DB, string) (bool, bool, error) {
			return true, true, nil
		},
	}

	allowed, err := authorizer.Authorize(
		t.Context(), uuid.NewString(), intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
		auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())},
	)

	require.NoError(t, err)
	require.False(t, allowed)
	require.Len(t, checker.calls, 1)
	wantCan, err := authorizer.can.forPermission(
		checker.calls[0].can.Resource().ID(),
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
		true,
	)
	require.NoError(t, err)
	require.Equal(t, wantCan.EngineKey(), checker.calls[0].can.EngineKey())
}

func TestResourceAuthorizerKeepsLifecycleAndPermissionFailuresTyped(t *testing.T) {
	t.Parallel()
	dependencyFailure := errors.New("unavailable")

	t.Run("lifecycle", func(t *testing.T) {
		checker := &recordingPermissionChecker{allowed: true}
		authorizer := resourceAuthorizer{
			db: newAuthorizationUnitDB(t), checker: checker,
			can: collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived},
			readRoot: func(context.Context, *gorm.DB, string) (bool, bool, error) {
				return false, false, dependencyFailure
			},
		}

		_, err := authorizer.Authorize(t.Context(), uuid.NewString(), intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW, auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())})

		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		require.Empty(t, checker.calls)
	})

	t.Run("permission", func(t *testing.T) {
		checker := &recordingPermissionChecker{err: dependencyFailure}
		authorizer := resourceAuthorizer{
			db: newAuthorizationUnitDB(t), checker: checker,
			can: collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived},
			readRoot: func(context.Context, *gorm.DB, string) (bool, bool, error) {
				return true, true, nil
			},
		}

		_, err := authorizer.Authorize(t.Context(), uuid.NewString(), intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())})

		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		require.Len(t, checker.calls, 1)
	})
}

func TestRegistryMapsEveryWireResourceToTypedSpiceDBResource(t *testing.T) {
	t.Parallel()
	db := newAuthorizationUnitDB(t)
	createRegistrationRootTables(t, db)
	checker := &recordingPermissionChecker{allowed: true}
	registry := NewRegistry(db, checker)
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}

	for _, spec := range registrationSpecs {
		spec := spec
		t.Run(spec.resourceType.String(), func(t *testing.T) {
			resourceID := uuid.NewString()
			insertRegistrationRoot(t, db, spec, resourceID)
			checker.calls = nil

			allowed, err := registry.Authorize(
				t.Context(), spec.resourceType, resourceID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, subject,
			)
			require.NoError(t, err)
			require.True(t, allowed)
			require.Len(t, checker.calls, 1)
			wantCan, canErr := spec.can.forPermission(resourceID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, false)
			require.NoError(t, canErr)
			require.Equal(t, wantCan.EngineKey(), checker.calls[0].can.EngineKey())
			require.Equal(t, subject.ID.String(), checker.calls[0].actor.AccountIdentityID())
		})
	}
}

func TestRegistryHidesMissingRootBeforeEverySpiceDBObjectCheck(t *testing.T) {
	t.Parallel()
	db := newAuthorizationUnitDB(t)
	createRegistrationRootTables(t, db)
	checker := &recordingPermissionChecker{allowed: true}
	registry := NewRegistry(db, checker)
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}

	for _, spec := range registrationSpecs {
		spec := spec
		t.Run(spec.resourceType.String(), func(t *testing.T) {
			checker.calls = nil
			allowed, err := registry.Authorize(
				t.Context(), spec.resourceType, uuid.NewString(), intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW, subject,
			)
			require.NoError(t, err)
			require.False(t, allowed)
			require.Empty(t, checker.calls)
		})
	}
}

func createRegistrationRootTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, spec := range registrationSpecs {
		definition := " (id TEXT PRIMARY KEY)"
		if spec.archivedStatus != "" {
			definition = " (id TEXT PRIMARY KEY, status TEXT NOT NULL)"
		}
		require.NoError(t, db.Exec("CREATE TABLE "+spec.table+definition).Error)
	}
}

func insertRegistrationRoot(t *testing.T, db *gorm.DB, spec registrationSpec, resourceID string) {
	t.Helper()
	if spec.archivedStatus == "" {
		require.NoError(t, db.Exec("INSERT INTO "+spec.table+" (id) VALUES (?)", resourceID).Error)
		return
	}
	require.NoError(t, db.Exec(
		"INSERT INTO "+spec.table+" (id, status) VALUES (?, ?)",
		resourceID,
		"ordinary",
	).Error)
}

func TestRegistryUsesArchivedPermissionsForEveryLifecycleDomain(t *testing.T) {
	t.Parallel()
	db := newAuthorizationUnitDB(t)
	for _, table := range []string{"post", "work", "program_event", "terms_history", "privacy_history"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id TEXT PRIMARY KEY, status TEXT NOT NULL)").Error)
	}
	checker := &recordingPermissionChecker{allowed: true}
	registry := NewRegistry(db, checker)
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}

	for _, testCase := range []struct {
		name         string
		resourceType intrav1.CollaborationResourceType
		can          collaborationCanSet
		table        string
		status       string
	}{
		{"post", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST, collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived}, "post", managev1.PostStatus_POST_STATUS_ARCHIVED.String()},
		{"work", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK, collaborationCanSet{view: policyv1.Work.View, edit: policyv1.Work.Edit, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived}, "work", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()},
		{"program event", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT, collaborationCanSet{view: policyv1.ProgramEvent.View, edit: policyv1.ProgramEvent.Edit, viewArchived: policyv1.ProgramEvent.ViewArchived, editArchived: policyv1.ProgramEvent.EditArchived}, "program_event", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()},
		{"terms", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY, collaborationCanSet{view: policyv1.TermsHistory.View, edit: policyv1.TermsHistory.Edit, viewArchived: policyv1.TermsHistory.ViewArchived, editArchived: policyv1.TermsHistory.EditArchived}, "terms_history", managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()},
		{"privacy", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY, collaborationCanSet{view: policyv1.PrivacyHistory.View, edit: policyv1.PrivacyHistory.Edit, viewArchived: policyv1.PrivacyHistory.ViewArchived, editArchived: policyv1.PrivacyHistory.EditArchived}, "privacy_history", managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			resourceID := uuid.NewString()
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.table+" (id, status) VALUES (?, ?)",
				resourceID,
				testCase.status,
			).Error)
			for _, permissionCase := range []intrav1.CollaborationPermission{
				intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
				intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
			} {
				checker.calls = nil
				allowed, err := registry.Authorize(
					t.Context(), testCase.resourceType, resourceID,
					permissionCase, subject,
				)
				require.NoError(t, err)
				require.True(t, allowed)
				require.Len(t, checker.calls, 1)
				wantCan, canErr := testCase.can.forPermission(resourceID, permissionCase, true)
				require.NoError(t, canErr)
				require.Equal(t, wantCan.EngineKey(), checker.calls[0].can.EngineKey())
				require.Equal(t, subject.ID.String(), checker.calls[0].actor.AccountIdentityID())
			}
		})
	}
}

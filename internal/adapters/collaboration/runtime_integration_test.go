//go:build integration

package collaborationadapter

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type collaborationAuthorizationCheck struct {
	actor policyv1.Actor
	can   policyv1.Can
}

// recordingCollaborationAuthorizationChecker records the typed boundary call
// while delegating it to the real, fully-consistent SpiceDB client. Product-role
// and resource-lifecycle checks deliberately do not exist here: ReBAC is their
// sole authority.
type recordingCollaborationAuthorizationChecker struct {
	delegate PermissionChecker
	calls    []collaborationAuthorizationCheck
}

func requireCollaborationAuthorizationCheck(
	t *testing.T,
	calls []collaborationAuthorizationCheck,
	wantCan policyv1.Can,
	identityID string,
) {
	t.Helper()
	require.Len(t, calls, 1)
	require.Equal(t, wantCan.EngineKey(), calls[0].can.EngineKey())
	require.Equal(t, identityID, calls[0].actor.AccountIdentityID())
}

func (c *recordingCollaborationAuthorizationChecker) CheckActorCan(
	ctx context.Context,
	actor policyv1.Actor,
	can policyv1.Can,
) (bool, error) {
	c.calls = append(c.calls, collaborationAuthorizationCheck{actor: actor, can: can})
	return c.delegate.CheckActorCan(ctx, actor, can)
}

type collaborationAuthorizationPrincipal struct {
	identityID string
	memberID   string
	sessionID  string
}

// seedCollaborationAuthorizationPrincipal creates the same account-identity,
// Member, and active Kratos session chain production resolves in one query.
func seedCollaborationAuthorizationPrincipal(
	t *testing.T,
	db *gorm.DB,
	onboarded bool,
) collaborationAuthorizationPrincipal {
	t.Helper()

	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Collaboration editor "+identityID[:8])
	require.NoError(t, db.Model(&model.Member{}).
		Where("id = ?", memberID).
		Update("onboarded", onboarded).Error)

	sessionID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, authentication_methods
		)
		VALUES (
			?::uuid, ?::uuid, TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 hour',
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '[]'::jsonb
		)
	`, sessionID, identityID).Error)

	return collaborationAuthorizationPrincipal{
		identityID: identityID,
		memberID:   memberID,
		sessionID:  sessionID,
	}
}

func collaborationAuthorizationRequest(
	sessionID string,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	permission intrav1.CollaborationPermission,
) *connect.Request[intrav1.AuthorizeCollaborationRequest] {
	return connect.NewRequest(&intrav1.AuthorizeCollaborationRequest{
		Principal:  &intrav1.CollaborationPrincipal{SessionId: sessionID},
		Resource:   &intrav1.CollaborationResource{Type: resourceType, Id: resourceID, Locale: "en"},
		Permission: permission,
	})
}

func attachCollaborationResourcePolicy(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
) {
	t.Helper()
	var mutation policyv1.RelationshipMutation
	var err error
	switch resourceType {
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST:
		mutation, err = policyv1.Post.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK:
		mutation, err = policyv1.Work.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE:
		mutation, err = policyv1.Release.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_LABEL:
		mutation, err = policyv1.Label.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_ARTIST:
		mutation, err = policyv1.Artist.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_FORM:
		mutation, err = policyv1.Form.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE:
		mutation, err = policyv1.Page.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN:
		mutation, err = policyv1.Campaign.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE:
		mutation, err = policyv1.EmailTemplate.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_LAYOUT:
		mutation, err = policyv1.EmailLayout.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY:
		mutation, err = policyv1.TermsHistory.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY:
		mutation, err = policyv1.PrivacyHistory.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MAP_THEME:
		mutation, err = policyv1.MapTheme.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT:
		mutation, err = policyv1.ProgramEvent.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU:
		mutation, err = policyv1.Menu.TouchPolicy(resourceID)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES:
		mutation, err = policyv1.PostSeries.TouchPolicy(resourceID)
	default:
		require.FailNow(t, "unsupported Collaboration policy resource", resourceType.String())
		return
	}
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func TestAuthorizeCollaborationDelegatesEverySupportedResourceToCanonicalSpiceDBSubject(t *testing.T) {
	db := newServiceIntegrationDB(t)
	principal := seedCollaborationAuthorizationPrincipal(t, db, true)
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, principal.identityID, policyv1.Role.Admin())

	for _, spec := range registrationSpecs {
		spec := spec
		t.Run(spec.resourceType.String(), func(t *testing.T) {
			resourceID := seedCollaborationRoot(t, db, spec.resourceType)
			attachCollaborationResourcePolicy(t, spiceDB, spec.resourceType, resourceID)
			checker := &recordingCollaborationAuthorizationChecker{delegate: spiceDB}

			for _, permissionCase := range []intrav1.CollaborationPermission{
				intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
				intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
			} {
				checker.calls = nil
				response, err := newAuthorizationService(db, checker, "").AuthorizeCollaboration(
					t.Context(), collaborationAuthorizationRequest(principal.sessionID, spec.resourceType, resourceID, permissionCase),
				)

				require.NoError(t, err)
				require.True(t, response.Msg.Authorized)
				require.Equal(t, principal.memberID, response.Msg.GetMember().GetId())
				require.Equal(t, "en", response.Msg.GetLocale())
				wantCan, canErr := spec.can.forPermission(resourceID, permissionCase, false)
				require.NoError(t, canErr)
				requireCollaborationAuthorizationCheck(
					t,
					checker.calls,
					wantCan,
					principal.identityID,
				)
			}
		})
	}
}

func TestAuthorizeCollaborationDeniesWhenSpiceDBDeniesEdit(t *testing.T) {
	db := newServiceIntegrationDB(t)
	principal := seedCollaborationAuthorizationPrincipal(t, db, true)
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, principal.identityID, policyv1.Role.Admin())
	checker := &recordingCollaborationAuthorizationChecker{delegate: spiceDB}
	resourceID := seedCollaborationRoot(
		t,
		db,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
	)

	response, err := newAuthorizationService(db, checker, "").AuthorizeCollaboration(
		t.Context(),
		collaborationAuthorizationRequest(
			principal.sessionID,
			intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
			resourceID,
			intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
		),
	)

	require.NoError(t, err)
	require.False(t, response.Msg.Authorized)
	require.Equal(t,
		intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_PERMISSION_DENIED,
		response.Msg.DenialReason,
	)
	wantCan, err := policyv1.Page.Edit(resourceID)
	require.NoError(t, err)
	requireCollaborationAuthorizationCheck(
		t, checker.calls, wantCan, principal.identityID,
	)
}

func TestAuthorizeCollaborationRejectsInvalidOrNonOnboardedSessionBeforeSpiceDB(t *testing.T) {
	db := newServiceIntegrationDB(t)
	nonOnboarded := seedCollaborationAuthorizationPrincipal(t, db, false)
	spiceDB := integrationSpiceDB(t)

	for _, testCase := range []struct {
		name      string
		sessionID string
	}{
		{"malformed session id", "not-a-session-id"},
		{"unknown session", uuid.NewString()},
		{"non-onboarded member", nonOnboarded.sessionID},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			checker := &recordingCollaborationAuthorizationChecker{delegate: spiceDB}

			response, err := newAuthorizationService(db, checker, "").AuthorizeCollaboration(
				t.Context(),
				collaborationAuthorizationRequest(
					testCase.sessionID,
					intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
					uuid.NewString(),
					intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
				),
			)

			require.NoError(t, err)
			require.False(t, response.Msg.Authorized)
			require.Equal(t,
				intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_SESSION_INVALID,
				response.Msg.DenialReason,
			)
			require.Empty(t, checker.calls)
		})
	}
}

func TestAuthorizeCollaborationUsesArchivedObjectPermissionsForEveryLifecycleDomainIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	author := seedCollaborationAuthorizationPrincipal(t, db, true)
	nonAuthor := seedCollaborationAuthorizationPrincipal(t, db, true)
	admin := seedCollaborationAuthorizationPrincipal(t, db, true)
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, author.identityID, policyv1.Role.Author())
	grantIntegrationGlobalRole(t, spiceDB, admin.identityID, policyv1.Role.Admin())

	for _, testCase := range []struct {
		name         string
		resourceType intrav1.CollaborationResourceType
		can          collaborationCanSet
		seed         func(*testing.T, *gorm.DB, bool) string
	}{
		{"post", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST, collaborationCanSet{view: policyv1.Post.View, edit: policyv1.Post.Edit, viewArchived: policyv1.Post.ViewArchived, editArchived: policyv1.Post.EditArchived}, seedArchivedCollaborationPost},
		{"work", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK, collaborationCanSet{view: policyv1.Work.View, edit: policyv1.Work.Edit, viewArchived: policyv1.Work.ViewArchived, editArchived: policyv1.Work.EditArchived}, seedArchivedCollaborationWork},
		{"program event", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT, collaborationCanSet{view: policyv1.ProgramEvent.View, edit: policyv1.ProgramEvent.Edit, viewArchived: policyv1.ProgramEvent.ViewArchived, editArchived: policyv1.ProgramEvent.EditArchived}, seedArchivedCollaborationProgramEvent},
		{"terms", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY, collaborationCanSet{view: policyv1.TermsHistory.View, edit: policyv1.TermsHistory.Edit, viewArchived: policyv1.TermsHistory.ViewArchived, editArchived: policyv1.TermsHistory.EditArchived}, seedArchivedCollaborationTerms},
		{"privacy", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY, collaborationCanSet{view: policyv1.PrivacyHistory.View, edit: policyv1.PrivacyHistory.Edit, viewArchived: policyv1.PrivacyHistory.ViewArchived, editArchived: policyv1.PrivacyHistory.EditArchived}, seedArchivedCollaborationPrivacy},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			archivedID := testCase.seed(t, db, true)
			attachCollaborationResourcePolicy(t, spiceDB, testCase.resourceType, archivedID)
			checker := &recordingCollaborationAuthorizationChecker{delegate: spiceDB}
			service := newAuthorizationService(db, checker, "")

			for _, room := range []string{"source locale room", "target locale room"} {
				t.Run(room, func(t *testing.T) {
					checker.calls = nil
					view, err := service.AuthorizeCollaboration(t.Context(), collaborationAuthorizationRequest(
						author.sessionID, testCase.resourceType, archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
					))
					require.NoError(t, err)
					require.True(t, view.Msg.Authorized)
					wantCan, canErr := testCase.can.forPermission(archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW, true)
					require.NoError(t, canErr)
					requireCollaborationAuthorizationCheck(
						t, checker.calls, wantCan, author.identityID,
					)
				})
			}

			checker.calls = nil
			edit, err := service.AuthorizeCollaboration(t.Context(), collaborationAuthorizationRequest(
				author.sessionID, testCase.resourceType, archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
			))
			require.NoError(t, err)
			require.False(t, edit.Msg.Authorized)
			wantCan, canErr := testCase.can.forPermission(archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, true)
			require.NoError(t, canErr)
			requireCollaborationAuthorizationCheck(
				t, checker.calls, wantCan, author.identityID,
			)

			checker.calls = nil
			adminEdit, err := service.AuthorizeCollaboration(t.Context(), collaborationAuthorizationRequest(
				admin.sessionID, testCase.resourceType, archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
			))
			require.NoError(t, err)
			require.True(t, adminEdit.Msg.Authorized)
			wantCan, canErr = testCase.can.forPermission(archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, true)
			require.NoError(t, canErr)
			requireCollaborationAuthorizationCheck(
				t, checker.calls, wantCan, admin.identityID,
			)

			checker.calls = nil
			nonAuthorView, err := service.AuthorizeCollaboration(t.Context(), collaborationAuthorizationRequest(
				nonAuthor.sessionID, testCase.resourceType, archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
			))
			require.NoError(t, err)
			require.False(t, nonAuthorView.Msg.Authorized)
			wantCan, canErr = testCase.can.forPermission(archivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW, true)
			require.NoError(t, canErr)
			requireCollaborationAuthorizationCheck(
				t, checker.calls, wantCan, nonAuthor.identityID,
			)

			checker.calls = nil
			nonArchivedID := testCase.seed(t, db, false)
			nonArchivedView, err := service.AuthorizeCollaboration(t.Context(), collaborationAuthorizationRequest(
				author.sessionID, testCase.resourceType, nonArchivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
			))
			require.NoError(t, err)
			require.False(t, nonArchivedView.Msg.Authorized)
			wantCan, canErr = testCase.can.forPermission(nonArchivedID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW, false)
			require.NoError(t, canErr)
			requireCollaborationAuthorizationCheck(
				t, checker.calls, wantCan, author.identityID,
			)
		})
	}
}

func seedArchivedCollaborationPost(t *testing.T, db *gorm.DB, archived bool) string {
	id := seedInternalPostBaseRow(t, db)
	if archived {
		require.NoError(t, db.Table("post").Where("id = ?", id).Update("status", managev1.PostStatus_POST_STATUS_ARCHIVED.String()).Error)
	}
	return id
}

func seedArchivedCollaborationWork(t *testing.T, db *gorm.DB, archived bool) string {
	t.Helper()
	id, documentID := uuid.NewString(), seedServiceIntegrationContentDocument(t, db, "work")
	status := managev1.WorkStatus_WORK_STATUS_DRAFT.String()
	if archived {
		status = managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()
	}
	require.NoError(t, db.Exec(`INSERT INTO work (id, type, year, month, is_present, status, content_document_id) VALUES (?, 'WORK_TYPE_MUSIC_PROJECT', 2024, 1, TRUE, ?, ?)`, id, status, documentID).Error)
	return id
}

func seedArchivedCollaborationProgramEvent(t *testing.T, db *gorm.DB, archived bool) string {
	t.Helper()
	id, typeID, documentID := uuid.NewString(), uuid.NewString(), seedServiceIntegrationContentDocument(t, db, "program_event")
	status := managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String()
	if archived {
		status = managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()
	}
	require.NoError(t, db.Exec(`INSERT INTO program_event_type (id, slug, status) VALUES (?, ?, 'PROGRAM_EVENT_TYPE_STATUS_ACTIVE')`, typeID, "type-"+typeID).Error)
	require.NoError(t, db.Exec(`INSERT INTO program_event (id, slug, status, type_id, starts_at, timezone, location_mode, content_document_id) VALUES (?, ?, ?, ?, NOW(), 'UTC', 'PROGRAM_EVENT_LOCATION_MODE_ONLINE', ?)`, id, "event-"+id, status, typeID, documentID).Error)
	return id
}

func seedArchivedCollaborationTerms(t *testing.T, db *gorm.DB, archived bool) string {
	t.Helper()
	id, documentID := uuid.NewString(), seedServiceIntegrationContentDocument(t, db, "policy")
	var version int
	require.NoError(t, db.Raw("SELECT COALESCE(MAX(version), 0) + 1 FROM terms_history").Scan(&version).Error)
	status := managev1.TermsStatus_TERMS_STATUS_DRAFT.String()
	if archived {
		status = managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()
	}
	require.NoError(t, db.Exec(`INSERT INTO terms_history (id, version, content, status, content_document_id) VALUES (?, ?, '', ?, ?)`, id, version, status, documentID).Error)
	return id
}

func seedArchivedCollaborationPrivacy(t *testing.T, db *gorm.DB, archived bool) string {
	t.Helper()
	id, documentID := uuid.NewString(), seedServiceIntegrationContentDocument(t, db, "policy")
	var version int
	require.NoError(t, db.Raw("SELECT COALESCE(MAX(version), 0) + 1 FROM privacy_history").Scan(&version).Error)
	status := managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String()
	if archived {
		status = managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()
	}
	require.NoError(t, db.Exec(`INSERT INTO privacy_history (id, version, content, status, content_document_id) VALUES (?, ?, '', ?, ?)`, id, version, status, documentID).Error)
	return id
}

func seedCollaborationRoot(
	t *testing.T,
	db *gorm.DB,
	resourceType intrav1.CollaborationResourceType,
) string {
	t.Helper()
	switch resourceType {
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST:
		return seedArchivedCollaborationPost(t, db, false)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK:
		return seedArchivedCollaborationWork(t, db, false)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT:
		return seedArchivedCollaborationProgramEvent(t, db, false)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY:
		return seedArchivedCollaborationTerms(t, db, false)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY:
		return seedArchivedCollaborationPrivacy(t, db, false)
	}

	id := uuid.NewString()
	switch resourceType {
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec(
			`INSERT INTO release (id, type, content_document_id) VALUES (?, 'RELEASE_TYPE_ALBUM', ?)`,
			id, documentID,
		).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_LABEL:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec("INSERT INTO label (id, content_document_id) VALUES (?, ?)", id, documentID).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_ARTIST:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec("INSERT INTO artist (id, content_document_id) VALUES (?, ?)", id, documentID).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_FORM:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec("INSERT INTO form (id, content_document_id) VALUES (?, ?)", id, documentID).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE:
		documentID := seedServiceIntegrationContentDocument(t, db, "page")
		require.NoError(t, db.Exec("INSERT INTO page (id, content_document_id) VALUES (?, ?)", id, documentID).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN:
		documentID := seedServiceIntegrationContentDocument(t, db, "email")
		require.NoError(t, db.Exec(
			"INSERT INTO campaign (id, target_mode, content_document_id) VALUES (?, 'all', ?)",
			id, documentID,
		).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE:
		documentID := seedServiceIntegrationContentDocument(t, db, "email")
		require.NoError(t, db.Exec(
			"INSERT INTO email_template (id, key, name, content_document_id) VALUES (?, ?, ?, ?)",
			id, "collaboration-"+id, "Collaboration template "+id, documentID,
		).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_LAYOUT:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec(
			"INSERT INTO email_layout (id, key, name, content_document_id) VALUES (?, ?, ?, ?)",
			id, "collaboration-"+id, "Collaboration layout "+id, documentID,
		).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MAP_THEME:
		variant := collaborationMapThemeVariant("#000000")
		require.NoError(t, db.Create(&model.MapTheme{
			ID: id, Name: "Collaboration theme " + id,
			CalloutScale: 1, CalloutFields: pq.StringArray{"name"}, AttributionFontSize: 11,
			EditVersion: 1, LightVariant: variant, DarkVariant: variant,
		}).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec(
			"INSERT INTO menu (id, name, content_document_id) VALUES (?, ?, ?)",
			id, "Collaboration menu "+id, documentID,
		).Error)
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES:
		documentID := seedServiceIntegrationContentDocument(t, db, "compact")
		require.NoError(t, db.Exec(
			"INSERT INTO series (id, slug, content_document_id) VALUES (?, ?, ?)",
			id, "collaboration-"+id, documentID,
		).Error)
	default:
		t.Fatalf("unsupported collaboration root fixture: %s", resourceType)
	}
	return id
}

func collaborationMapThemeVariant(color string) model.MapThemeVariant {
	return model.MapThemeVariant{
		BackgroundColor: color, WaterColor: color, LandColor: color, RoadColor: color,
		BuildingFillColor: color, BuildingStrokeColor: color,
		CalloutLineColor: color, CalloutTextColor: color, CalloutBackgroundColor: color,
		CalloutDescriptionColor: color, AttributionColor: color, LabelTextColor: color,
		ClusterColor: color, ClusterHoverColor: color, ClusterTextColor: color,
		ClusterTextHoverColor: color, CalloutHoverLineColor: color,
		CalloutHoverTextColor: color, CalloutHoverDescriptionColor: color,
		CalloutHoverBackgroundColor: color,
	}
}

func TestAuthorizeCollaborationRejectsInvalidPermissionOrResourceIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	principal := seedCollaborationAuthorizationPrincipal(t, db, true)
	service := newAuthorizationService(db, &recordingCollaborationAuthorizationChecker{delegate: integrationSpiceDB(t)}, "")
	for _, testCase := range []struct {
		name       string
		resource   intrav1.CollaborationResourceType
		permission intrav1.CollaborationPermission
	}{
		{"invalid permission", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_UNSPECIFIED},
		{"invalid resource", intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_UNSPECIFIED, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.AuthorizeCollaboration(t.Context(), collaborationAuthorizationRequest(principal.sessionID, testCase.resource, uuid.NewString(), testCase.permission))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestAuthorizeCollaborationRejectsNonCanonicalLocaleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	principal := seedCollaborationAuthorizationPrincipal(t, db, true)
	checker := &recordingCollaborationAuthorizationChecker{delegate: integrationSpiceDB(t)}
	service := newAuthorizationService(db, checker, "")
	request := collaborationAuthorizationRequest(
		principal.sessionID,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		uuid.NewString(),
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
	)
	request.Msg.Resource.Locale = "EN"

	_, err := service.AuthorizeCollaboration(t.Context(), request)

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, checker.calls)
}

func TestAuthorizeCollaborationArchivedPostRejectsResourceCollaboratorViewIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	collaborator := seedCollaborationAuthorizationPrincipal(t, db, true)
	spiceDB := integrationSpiceDB(t)
	postID := seedArchivedCollaborationPost(t, db, true)
	actor, err := policyv1.NewAccountIdentityActor(collaborator.identityID)
	require.NoError(t, err)
	relation, err := policyv1.Post.TouchCollaborator(postID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), relation)
	require.NoError(t, err)
	checker := &recordingCollaborationAuthorizationChecker{delegate: spiceDB}

	response, err := newAuthorizationService(db, checker, "").AuthorizeCollaboration(
		t.Context(), collaborationAuthorizationRequest(collaborator.sessionID, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST, postID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW),
	)
	require.NoError(t, err)
	require.False(t, response.Msg.Authorized)
	wantCan, err := policyv1.Post.ViewArchived(postID)
	require.NoError(t, err)
	requireCollaborationAuthorizationCheck(
		t, checker.calls, wantCan, collaborator.identityID,
	)
}

func TestAuthorizeCollaborationHidesEveryMissingRootWithoutSpiceDBFallbackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	principal := seedCollaborationAuthorizationPrincipal(t, db, true)
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, principal.identityID, policyv1.Role.Admin())

	for _, spec := range registrationSpecs {
		spec := spec
		t.Run(spec.resourceType.String(), func(t *testing.T) {
			checker := &recordingCollaborationAuthorizationChecker{delegate: spiceDB}
			response, err := newAuthorizationService(db, checker, "").AuthorizeCollaboration(
				t.Context(),
				collaborationAuthorizationRequest(
					principal.sessionID,
					spec.resourceType,
					uuid.NewString(),
					intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
				),
			)

			require.NoError(t, err)
			require.False(t, response.Msg.Authorized)
			require.Equal(t,
				intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_PERMISSION_DENIED,
				response.Msg.DenialReason,
			)
			require.Empty(t, checker.calls)
		})
	}
}

func newAuthorizationService(db *gorm.DB, checker PermissionChecker, cdnDomain string) *collaboration.Service {
	runtime := NewRuntime(db, checker, cdnDomain)
	return collaboration.NewService(db, runtime.Registry, runtime.Members)
}

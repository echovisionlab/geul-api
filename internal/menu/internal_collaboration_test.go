package menu

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type menuCollaborationAudit struct{ records []sharedtelemetry.AuditRecord }

func (a *menuCollaborationAudit) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	a.records = append(a.records, record)
	return nil
}

var _ domainaudit.Appender = (*menuCollaborationAudit)(nil)

type menuCollaborationTargets struct{}

func (menuCollaborationTargets) ValidateAndLock(context.Context, *gorm.DB, []TargetReference) error {
	return nil
}

type menuCollaborationContributorResolver struct {
	subject auth.AccountIdentitySubject
}

func (r menuCollaborationContributorResolver) ResolveActiveSubjects(
	_ context.Context,
	_ *gorm.DB,
	members []string,
) (map[string]auth.AccountIdentitySubject, error) {
	result := make(map[string]auth.AccountIdentitySubject, len(members))
	for _, member := range members {
		result[member] = r.subject
	}
	return result, nil
}

type menuCollaborationAuthorizer struct {
	permissions []intrav1.CollaborationPermission
}

func (a *menuCollaborationAuthorizer) Authorize(
	_ context.Context,
	_ string,
	permission intrav1.CollaborationPermission,
	_ auth.AccountIdentitySubject,
) (bool, error) {
	a.permissions = append(a.permissions, permission)
	return true, nil
}

func (a *menuCollaborationAuthorizer) AuthorizeInTx(
	ctx context.Context,
	_ *gorm.DB,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	return a.Authorize(ctx, resourceID, permission, subject)
}

func TestInternalMenuServicePersistsExactTargetAndSourceRooms(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	menuID, itemID := uuid.NewString(), uuid.NewString()
	seedMenuAIDocumentStateTestRoot(
		t, db, menuID, "en",
		[]byte(`[{"id":"`+itemID+`","label":"legacy","linkType":"custom","url":"/posts"}]`),
		now,
	)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, 'en', ?, ?, ?), (?, 'ko', '[]', ?, ?)`,
		menuID, `[{"id":"`+itemID+`","label":"Posts"}]`, now, now,
		menuID, now, now,
	).Error)

	audit := &menuCollaborationAudit{}
	authorizer := &menuCollaborationAuthorizer{}
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}
	registry := collaboration.NewRegistry(collaboration.Registration{
		ResourceType: intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU,
		Authorizer:   authorizer,
	})
	checkpoint := collaboration.NewCheckpointFence(
		registry, menuCollaborationContributorResolver{subject: subject},
	)
	service := NewInternalMenuService(&MenuService{
		db: db, targets: menuCollaborationTargets{}, auditWriter: audit,
	}, checkpoint)
	memberID := uuid.NewString()

	target, err := service.LoadDocument(t.Context(), connect.NewRequest(
		&intrav1.LoadMenuDocumentRequest{MenuId: menuID, Locale: "ko"},
	))
	require.NoError(t, err)
	require.True(t, target.Msg.LocaleExists)
	require.NotNil(t, target.Msg.TargetRevision)
	targetSave, err := service.SaveDocument(t.Context(), connect.NewRequest(
		&intrav1.SaveMenuDocumentRequest{
			MenuId: menuID, Locale: "ko", Name: target.Msg.Name,
			Items: target.Msg.Items, RequestedLabels: map[string]string{itemID: "게시물"},
			ContributorMemberIds:     []string{memberID},
			ExpectedDocumentRevision: target.Msg.DocumentRevision,
			ExpectedTargetRevision:   target.Msg.TargetRevision,
		},
	))
	require.NoError(t, err)
	require.Equal(t, target.Msg.DocumentRevision, targetSave.Msg.DocumentRevision)
	require.NotEqual(t, target.Msg.TargetRevision, targetSave.Msg.TargetRevision)
	require.Equal(t, []intrav1.CollaborationPermission{
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT,
	}, authorizer.permissions)

	source, err := service.LoadDocument(t.Context(), connect.NewRequest(
		&intrav1.LoadMenuDocumentRequest{MenuId: menuID, Locale: "en"},
	))
	require.NoError(t, err)
	newID := uuid.NewString()
	source.Msg.Items = append(source.Msg.Items, &intrav1.MenuCollaborationItem{
		Id: newID, LinkType: "custom", Url: stringPointer("/about"),
	})
	source.Msg.RequestedLabels[newID] = "About"
	sourceSave, err := service.SaveDocument(t.Context(), connect.NewRequest(
		&intrav1.SaveMenuDocumentRequest{
			MenuId: menuID, Locale: "en", Name: source.Msg.Name,
			Items: source.Msg.Items, RequestedLabels: source.Msg.RequestedLabels,
			ContributorMemberIds:     []string{memberID},
			ExpectedDocumentRevision: source.Msg.DocumentRevision,
		},
	))
	require.NoError(t, err)
	require.NotEqual(t, source.Msg.DocumentRevision, sourceSave.Msg.DocumentRevision)
	require.Equal(t, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE, authorizer.permissions[1])
	require.Len(t, audit.records, 2)
	require.Equal(t, "ko", audit.records[0].Locale)
}

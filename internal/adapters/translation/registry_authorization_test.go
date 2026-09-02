package translationadapter

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSourceEditAuthorizationNeverFallsThroughForUnmappedDomain(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		Authenticated: true,
		IdentityID:    "3219977a-0bb0-46fb-a6d4-c36d0e70c718",
		MemberID:      "d283bd0d-878c-413e-a769-ea9bec7c21a3",
	})
	err := (&DomainRegistry{}).RequireSourceLocaleEdit(
		ctx, &gorm.DB{}, &auth.SpiceDBClient{}, "unknown", "entity-1",
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestTranslationInteractiveCanCatalogIsComplete(t *testing.T) {
	registrations := defaultDomainRegistrations(nil, nil)
	ports, err := buildDomainPorts(registrations)
	require.NoError(t, err)
	require.Len(t, ports, len(core.Definitions()))
	for _, definition := range core.Definitions() {
		port, ok := ports[definition.Kind]
		require.True(t, ok, definition.Kind)
		require.NotNil(t, port.loadSourceDocument, definition.Kind)
	}
}

func TestTranslationDomainRegistryRejectsIncompleteComposition(t *testing.T) {
	complete := defaultDomainRegistrations(nil, nil)
	_, err := buildDomainPorts(complete[:len(complete)-1])
	require.ErrorContains(t, err, "missing")

	duplicate := append(append([]domainRegistration(nil), complete...), complete[0])
	_, err = buildDomainPorts(duplicate)
	require.ErrorContains(t, err, "more than once")

	_, err = buildDomainPorts([]domainRegistration{{domain: core.KindPost}})
	require.ErrorContains(t, err, "required")

	_, err = buildDomainPorts([]domainRegistration{{domain: core.Kind("unknown"), port: complete[0].port}})
	require.ErrorContains(t, err, "unsupported")
	require.False(t, strings.Contains(err.Error(), "missing"))
}

func TestTranslationEditPermissionUsesArchivedActionExclusively(t *testing.T) {
	resourceID := "22222222-2222-4222-8222-222222222222"
	permissions := translationCanSet{
		edit:         policyv1.Work.Edit,
		editArchived: policyv1.Work.EditArchived,
	}
	regular, err := permissions.edit(resourceID)
	require.NoError(t, err)
	archived, err := permissions.editArchived(resourceID)
	require.NoError(t, err)
	require.NotEqual(t, regular.EngineKey(), archived.EngineKey())
}

func TestTranslationAuthorizationDenialIsMaskedAsNotFound(t *testing.T) {
	err := maskTranslationAuthorizationDenial(
		errs.NoPermission("edit", "post"), "post", "post-1",
	)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

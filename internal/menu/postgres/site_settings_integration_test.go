//go:build integration

package menupostgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func memberContext(t *testing.T) context.Context {
	t.Helper()
	identityID, memberID := uuid.NewString(), uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{IdentityID: identityID, MemberID: memberID, SessionID: uuid.NewString()})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func siteSettingsMenuService(t *testing.T, db *gorm.DB, audit *audittest.Recorder) *menu.Service {
	t.Helper()
	service, err := menu.NewService(menu.Deps{
		Units:   menupostgres.NewUnits(db, menupostgres.Ports{SiteSettings: menuadapter.NewSiteSettingsReferences(audit)}),
		SpiceDB: authztest.AllowAll(), Fence: &identitystatetest.Fence{}, Audit: audit,
	})
	require.NoError(t, err)
	return service
}

func siteSettingsAudits(audit *audittest.Recorder) []sharedtelemetry.AuditRecord {
	var records []sharedtelemetry.AuditRecord
	for _, record := range audit.Records() {
		if record.Action == sharedtelemetry.AuditSiteSettingsUpdated {
			records = append(records, record)
		}
	}
	return records
}

// Deleting a Menu clears the Site Settings slots that select it in the same
// transaction, through the Site Settings adapter the Store is bound to.
func TestMenuDeleteUnbindsTheSelectedSlotsInItsTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	ctx := memberContext(t)
	audit := &audittest.Recorder{}
	service := siteSettingsMenuService(t, db, audit)
	slots := func() map[string]*string {
		var row struct {
			Header, Secondary, Footer, Avatar *string
		}
		require.NoError(t, db.Raw(`SELECT menu_header_id::text AS header, menu_secondary_id::text AS secondary,
			menu_footer_id::text AS footer, menu_avatar_dropdown_id::text AS avatar FROM site_settings WHERE id = 1`).Scan(&row).Error)
		return map[string]*string{"header": row.Header, "secondary": row.Secondary, "footer": row.Footer, "avatar": row.Avatar}
	}

	bound, err := service.CreateMenu(ctx, menu.CreateMenuCommand{Name: "Bound"})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE site_settings SET menu_header_id = ?::uuid, menu_footer_id = ?::uuid, menu_avatar_dropdown_id = ?::uuid WHERE id = 1`,
		bound.ID, bound.ID, bound.ID).Error)
	require.NoError(t, service.DeleteMenu(ctx, menu.DeleteMenuCommand{ID: bound.ID}))
	require.Equal(t, map[string]*string{"header": nil, "secondary": nil, "footer": nil, "avatar": nil}, slots())
	records := siteSettingsAudits(audit)
	require.Len(t, records, 1)
	require.Equal(t, []string{"menu_avatar_dropdown_id", "menu_footer_id", "menu_header_id"}, records[0].ChangedFields)

	unbound, err := service.CreateMenu(ctx, menu.CreateMenuCommand{Name: "Unbound"})
	require.NoError(t, err)
	require.NoError(t, service.DeleteMenu(ctx, menu.DeleteMenuCommand{ID: unbound.ID}))
	require.Len(t, siteSettingsAudits(audit), 1, "a Menu no slot selects leaves Site Settings unaudited")

	kept, err := service.CreateMenu(ctx, menu.CreateMenuCommand{Name: "Kept"})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE site_settings SET menu_secondary_id = ?::uuid WHERE id = 1`, kept.ID).Error)
	auditDown := errors.New("audit unavailable")
	err = siteSettingsMenuService(t, db, &audittest.Recorder{Err: auditDown}).DeleteMenu(ctx, menu.DeleteMenuCommand{ID: kept.ID})
	require.ErrorIs(t, err, auditDown)
	require.Equal(t, kept.ID, *slots()["secondary"], "a failed delete rolls the cleared slot back")
	var count int64
	require.NoError(t, db.Table("menu").Where("id = ?", kept.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

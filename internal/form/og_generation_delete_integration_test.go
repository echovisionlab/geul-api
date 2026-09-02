//go:build integration

package form_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestDeleteFormRollsBackWhenDurableOgCancellationFailsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	formID := seedFormSourceLocaleBaseRowAt(t, db, time.Now().UTC())
	var form model.Form
	require.NoError(t, db.First(&form, "id = ?", formID).Error)

	locale := "en"
	planner := og.NewPlanner(db, "https://cdn.example.com", formIntegrationRenderConfig{}, formogadapter.NewProjection())
	request := og.Request{Target: og.Target{EntityType: "form", EntityID: form.ID, Locale: &locale, Kind: "locale"}, Title: "Delete rollback fixture"}
	plan, err := planner.RequestBulk(t.Context(), "automatic", "delete rollback fixture", []og.Request{request}, func(context.Context, *gorm.DB) ([]og.Request, error) {
		return []og.Request{request}, nil
	})
	require.NoError(t, err)
	require.Len(t, plan.GenerationIDs, 1)

	// Force the first durable cancellation write to fail. The service must
	// roll its earlier share/source cleanup and the form deletion back to the
	// savepoint instead of returning success with a live generation.
	require.NoError(t, db.Exec(`
CREATE FUNCTION pg_temp.fail_test_og_cancel() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'forced OG cancellation failure';
END;
$$`).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`
CREATE TRIGGER fail_test_og_cancel
BEFORE UPDATE ON og_generation
FOR EACH ROW
WHEN (OLD.id = '%s')
EXECUTE FUNCTION pg_temp.fail_test_og_cancel()`, plan.GenerationIDs[0])).Error)

	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	service := newFormServiceForIntegration(db, user.IdentityID.String(), spiceDB)
	_, err = service.DeleteForm(ctx, connect.NewRequest(&managev1.DeleteFormRequest{Id: form.ID}))
	require.Error(t, err)

	var formCount int64
	require.NoError(t, db.Model(&model.Form{}).Where("id = ?", form.ID).Count(&formCount).Error)
	require.Equal(t, int64(1), formCount)
	var generation model.OgGeneration
	require.NoError(t, db.First(&generation, "id = ?", plan.GenerationIDs[0]).Error)
	require.Equal(t, model.OgGenerationStatusQueued, generation.Status)
}

//go:build integration

package campaignpostgres_test

import (
	"context"
	"testing"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign"
	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This checks the production Email Authoring adapter's row lock behind Ports.
func TestLayoutPortLocksTheReferencedLayoutIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	contents := testutil.NewEmailContentBlockStore(t, nil)
	var layout model.EmailLayout
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		doc, err := contents.CreateDocument(t.Context(), tx, contentblock.CreateInput{Profile: "compact", SourceLocale: "en"})
		if err != nil {
			return err
		}
		layout = model.EmailLayout{Name: "Campaign layout", Key: uuid.NewString(), ContentDocumentID: doc.Document.ID.String(), SourceLocale: "en"}
		return tx.Create(&layout).Error
	}))
	ports := campaignpostgres.Ports{ContentBlocks: contents, EmailAuthoring: campaignadapter.NewEmailAuthoring()}
	units := mutation.Postgres(db, func(tx *gorm.DB) campaign.Store { return campaignpostgres.NewStore(tx, ports) })
	h := mutationtest.PostgresHarness(db, units)
	lock := func(ctx context.Context, s campaign.Store) error {
		rows, err := s.LockLayouts(ctx, layout.ID, layout.ID, uuid.NewString())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, layout.ID, rows[layout.ID].ID)
		return nil
	}
	require.NoError(t, mutationtest.RequireSecondWaits(t, h, lock, func(context.Context, campaign.Store) error { return nil }, lock))
}

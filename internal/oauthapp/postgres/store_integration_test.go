//go:build integration

package oauthapppostgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	oauthappadapter "github.com/echovisionlab/geul-api/internal/adapters/oauthapp"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
	oauthapppostgres "github.com/echovisionlab/geul-api/internal/oauthapp/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestStoreKeepsSignInAppsAndTheirCriteriaIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	units, err := oauthapppostgres.NewUnits(db)
	require.NoError(t, err)
	ctx := t.Context()
	testers, staff := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member_criteria (id, name, kind)
		VALUES (?, 'Testers', 'MEMBER_CRITERIA_KIND_MEMBER_TAGS'), (?, 'Staff', 'MEMBER_CRITERIA_KIND_FILTER')`, testers, staff).Error)

	write := func(change func(oauthapp.Store) error) error {
		return units.Transaction(ctx, false, func(tx mutation.Tx[oauthapp.Store]) error { return change(tx.Store()) })
	}
	var created oauthapp.Record
	require.NoError(t, write(func(store oauthapp.Store) (err error) {
		created, err = store.InsertApp(ctx, oauthapp.Record{ClientID: "monitor", Access: oauthapp.AccessMemberCriteria, MemberCriteriaIDs: []string{testers}})
		return err
	}))
	require.NotEmpty(t, created.Revision)
	require.ErrorIs(t, write(func(store oauthapp.Store) error {
		_, err := store.InsertApp(ctx, oauthapp.Record{ClientID: "monitor", Access: oauthapp.AccessNone})
		return err
	}), oauthapp.ErrAppExists)

	var updated oauthapp.Record
	require.NoError(t, write(func(store oauthapp.Store) (err error) {
		updated, err = store.UpdateApp(ctx, oauthapp.Record{ClientID: "monitor", Access: oauthapp.AccessMemberCriteria, MemberCriteriaIDs: []string{staff, testers}})
		return err
	}))
	require.NotEqual(t, created.Revision, updated.Revision)

	var read oauthapp.Record
	require.NoError(t, units.Read(ctx, func(store oauthapp.Store) (err error) {
		read, err = store.App(ctx, "monitor")
		return err
	}))
	require.ElementsMatch(t, []string{staff, testers}, read.MemberCriteriaIDs)
	require.Equal(t, updated.Revision, read.Revision)

	counts, err := oauthappadapter.SignInAppUsage{}.CountSignInApps(context.Background(), db, []string{testers, staff, uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, map[string]int32{testers: 1, staff: 1}, counts)

	require.NoError(t, write(func(store oauthapp.Store) error { return store.DeleteApp(ctx, "monitor") }))
	require.ErrorIs(t, units.Read(ctx, func(store oauthapp.Store) error {
		_, err := store.App(ctx, "monitor")
		return err
	}), oauthapp.ErrAppNotFound)
}

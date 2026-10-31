//go:build integration

package public_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"gorm.io/gorm"
)

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		ApplyAppSchemaSQL: true,
	}).DB
}

func newPublicLegalService(t *testing.T, db *gorm.DB, store *contentblock.Store, media legaldomain.PublicMedia) *legalpublic.Service {
	t.Helper()
	service, err := legalpublic.NewService(legalpublic.Deps{DB: db, ContentBlocks: store, Media: media})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newPublicPrivacyHandler(t *testing.T, db *gorm.DB, store *contentblock.Store, media legaldomain.PublicMedia) *legalpublic.PrivacyConnectHandler {
	t.Helper()
	return legalpublic.NewPrivacyConnectHandler(newPublicLegalService(t, db, store, media))
}

func newPublicTermsHandler(t *testing.T, db *gorm.DB, store *contentblock.Store, media legaldomain.PublicMedia) *legalpublic.TermsConnectHandler {
	t.Helper()
	return legalpublic.NewTermsConnectHandler(newPublicLegalService(t, db, store, media))
}

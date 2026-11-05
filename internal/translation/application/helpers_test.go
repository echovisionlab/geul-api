package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// env is the Translation use cases over one fake Store.
type env struct {
	store   *translationtest.Store
	units   *mutationtest.UnitOfWork[application.Store]
	checker *authztest.Checker
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	files   *recordingXLIFFFiles
	service *application.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, units := translationtest.New()
	e := &env{
		store: store, units: units, checker: authztest.AllowAll(),
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, files: &recordingXLIFFFiles{},
	}
	service, err := application.NewService(application.Deps{
		Units: units, SpiceDB: e.checker, Fence: e.fence, Audit: e.audit,
		Plans: translationtest.Plans{}, XLIFFFiles: e.files,
	})
	require.NoError(t, err)
	e.service = service
	return e
}

// principal is an authenticated, onboarded Member with a request context,
// as the Connect and MCP gateways hand it to a use case.
type principal struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newPrincipal(t *testing.T) principal {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

func codeOf(err error) connect.Code {
	return errs.Wrap(err).Code()
}

// seedSeries stores a Post Series with an English title and summary.
func (e *env) seedSeries(t *testing.T) *translationtest.Entity {
	t.Helper()
	return e.store.SeedEntity(translationtest.Entity{
		EntityType: "series", EntityID: uuid.NewString(), SourceLocale: "en",
		Source: &translation.SourceDocument{Title: "Night walks", Summary: stringPointer("Field recordings")},
	})
}

func stringPointer(value string) *string { return &value }

// recordingXLIFFFiles stores exports as a short-lived download and serves
// one upload.
type recordingXLIFFFiles struct {
	created []application.TranslationXLIFFArtifact
	upload  application.VerifiedTranslationXLIFF
}

func (f *recordingXLIFFFiles) CreateTranslationXLIFF(_ context.Context, artifact application.TranslationXLIFFArtifact) (*mediaauth.Reference, error) {
	f.created = append(f.created, artifact)
	return &mediaauth.Reference{
		FileID: uuid.NewString(), URL: "https://cdn.example/xliff/signed",
		ExpiresAt: time.Now().Add(time.Hour),
		Purpose:   mediaauth.PurposeDownload,
	}, nil
}

func (f *recordingXLIFFFiles) ReadVerifiedTranslationXLIFF(_ context.Context, fileID string, _ int64) (application.VerifiedTranslationXLIFF, error) {
	if f.upload.FileID != fileID {
		return application.VerifiedTranslationXLIFF{}, errors.New("no such upload")
	}
	return f.upload, nil
}

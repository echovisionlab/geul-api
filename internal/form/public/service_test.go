package public_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	"github.com/echovisionlab/geul-api/internal/form/public"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const publicSchema = `{"id":"public-form-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email address","type":"email"},{"id":"field-consent","key":"consent","label":"Consent","type":"checkbox"}]}]}`

type harness struct {
	store    *formtest.Store
	units    *mutationtest.UnitOfWork[public.Store]
	audit    *audittest.Recorder
	password *crypto.PasswordHasher
	service  *public.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, units := formtest.NewPublic()
	h := &harness{
		store: store, units: units, audit: &audittest.Recorder{},
		password: crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}),
	}
	service, err := public.NewService(public.Deps{
		Units: units, SpiceDB: authztest.AllowAll(), Fence: &identitystatetest.Fence{}, Audit: h.audit, Password: h.password,
	})
	require.NoError(t, err)
	h.service = service
	return h
}

// seedForm stores a published public Form with its source locale.
func (h *harness) seedForm(t *testing.T, change func(*form.Form)) form.Form {
	t.Helper()
	slug := "public-form-" + uuid.NewString()[:8]
	value := form.Form{Slug: &slug, Status: form.StatusPublished, IsPublic: true, SourceLocale: "en", AllowDuplicateSubmission: new(true)}
	if change != nil {
		change(&value)
	}
	stored := h.store.PutForm(value)
	h.store.PutLocale(stored.ID, "en", formtest.StoredLocale{Title: new("Public Password Form"), ContentJSON: []byte(publicSchema)})
	return stored
}

func (h *harness) hash(t *testing.T, password string) *string {
	t.Helper()
	hash, err := h.password.Hash(password)
	require.NoError(t, err)
	return &hash
}

func anonymous(t *testing.T) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.AnonymousActor{})
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), request)
}

func member(t *testing.T) (context.Context, string) {
	t.Helper()
	identityID, memberID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: identityID, MemberID: memberID, SessionID: sessionID,
	})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID), SessionID: auth.SessionID(sessionID),
		Authenticated: true, Onboarded: true,
	}), memberID
}

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, errs.Wrap(err).Code(), err.Error())
}

func TestPublicFormAccessPasswordSubmitDashboardAndLimit(t *testing.T) {
	h := newHarness(t)
	image := &form.Asset{ID: "featured", URL: "https://cdn.example.com/featured.webp"}
	ogAsset := &form.Asset{ID: uuid.NewString(), URL: "https://cdn.example.com/og.webp"}
	fileID := uuid.NewString()
	h.store.PutFile(fileID, image)
	h.store.PutReadyAsset(ogAsset)
	stored := h.seedForm(t, func(value *form.Form) {
		value.AccessPassword = h.hash(t, "correct-password")
		value.MaxSubmissions = new(int32(1))
		value.FeaturedImageFileID = &fileID
		value.OgAssetID = &ogAsset.ID
	})
	ctx := anonymous(t)

	missingPassword, err := h.service.CheckFormAccess(ctx, public.CheckFormAccessQuery{Slug: *stored.Slug})
	require.NoError(t, err)
	require.False(t, missingPassword.Accessible)
	require.Equal(t, public.ReasonPasswordRequired, missingPassword.Reason)
	require.NotNil(t, missingPassword.Form)
	require.True(t, missingPassword.Form.Metadata, "a password-gated Form shows only its metadata")
	require.Equal(t, ogAsset, missingPassword.Form.OgAsset)
	require.Equal(t, image, missingPassword.Form.FeaturedImage)

	valid, err := h.service.VerifyFormPassword(ctx, public.VerifyFormPasswordQuery{Slug: *stored.Slug, Password: "wrong-password"})
	require.NoError(t, err)
	require.False(t, valid)
	valid, err = h.service.VerifyFormPassword(ctx, public.VerifyFormPasswordQuery{Slug: *stored.Slug, Password: "correct-password"})
	require.NoError(t, err)
	require.True(t, valid)

	byID, err := h.service.CheckFormAccess(ctx, public.CheckFormAccessQuery{Slug: stored.ID, Password: new("correct-password")})
	require.NoError(t, err)
	require.True(t, byID.Accessible)
	require.False(t, byID.Form.Metadata)
	require.Equal(t, "Public Password Form", byID.Form.Document.Title)
	require.JSONEq(t, publicSchema, string(byID.Form.Document.Schema))

	_, err = h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Password: new("wrong-password"), Data: []byte(`{"email":"blocked@example.com"}`)})
	requireCode(t, err, connect.CodePermissionDenied)
	_, err = h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Password: new("correct-password"), Data: []byte(`{"unknown":"not-in-current-schema"}`)})
	requireCode(t, err, connect.CodeInvalidArgument)
	require.Empty(t, h.store.StoredSubmissions(stored.ID))

	submissionID, err := h.service.SubmitForm(ctx, public.SubmitFormCommand{
		FormID: stored.ID, Password: new("correct-password"), Data: []byte(`{"email":"hello@example.com"}`),
		ClientAddress: "203.0.113.7", UserAgent: " agent ", AcceptLanguage: "ko-KR,ko;q=0.9",
	})
	require.NoError(t, err)
	submissions := h.store.StoredSubmissions(stored.ID)
	require.Len(t, submissions, 1)
	require.Equal(t, submissionID, submissions[0].ID)
	require.Equal(t, "203.0.113.7", *submissions[0].IPAddress)
	require.Equal(t, "agent", *submissions[0].UserAgent)
	require.JSONEq(t, `{"email":"hello@example.com","__meta.locale":"ko"}`, string(submissions[0].Data))

	expires := time.Now().Add(time.Hour)
	token := uuid.NewString()
	h.store.PutShareLink(token, public.ShareLink{
		EntityID: stored.ID, EntityType: public.ShareLinkFormDashboard, ExpiresAt: &expires, PasswordHash: h.hash(t, "dashboard-password"),
	})
	missingProof, err := h.service.CheckFormAccess(ctx, public.CheckFormAccessQuery{Slug: *stored.Slug, ShareToken: &token, Dashboard: true})
	require.NoError(t, err)
	require.False(t, missingProof.Accessible)
	require.Equal(t, public.ReasonFormNotFound, missingProof.Reason)
	require.Nil(t, missingProof.Form)
	dashboardAccess, err := h.service.CheckFormAccess(ctx, public.CheckFormAccessQuery{
		Slug: *stored.Slug, ShareToken: &token, SharePassword: new("dashboard-password"), Dashboard: true,
	})
	require.NoError(t, err)
	require.True(t, dashboardAccess.Accessible)
	dashboard, err := h.service.GetFormDashboard(ctx, public.GetFormDashboardQuery{Slug: *stored.Slug, ShareToken: token, SharePassword: new("dashboard-password")})
	require.NoError(t, err)
	require.EqualValues(t, 1, dashboard.TotalSubmissions)
	require.Equal(t, *stored.Slug, dashboard.FormSlug)

	limited, err := h.service.CheckFormAccess(ctx, public.CheckFormAccessQuery{Slug: *stored.Slug, Password: new("correct-password")})
	require.NoError(t, err)
	require.False(t, limited.Accessible)
	require.Equal(t, public.ReasonMaxSubmissionsReached, limited.Reason)
	_, err = h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Password: new("correct-password"), Data: []byte(`{"email":"late@example.com"}`)})
	requireCode(t, err, connect.CodeFailedPrecondition)
}

func TestCheckFormAccessHidesMissingDraftAndPrivateForms(t *testing.T) {
	h := newHarness(t)
	missing, err := h.service.CheckFormAccess(anonymous(t), public.CheckFormAccessQuery{Slug: "missing"})
	require.NoError(t, err)
	require.Equal(t, public.Access{Reason: public.ReasonFormNotFound}, missing)

	draft := h.seedForm(t, func(value *form.Form) { value.Status = form.StatusDraft })
	access, err := h.service.CheckFormAccess(anonymous(t), public.CheckFormAccessQuery{Slug: *draft.Slug})
	require.NoError(t, err)
	require.Equal(t, public.Access{Reason: public.ReasonFormNotPublished}, access)

	private := h.seedForm(t, func(value *form.Form) { value.IsPublic = false })
	access, err = h.service.CheckFormAccess(anonymous(t), public.CheckFormAccessQuery{Slug: *private.Slug})
	require.NoError(t, err)
	require.Equal(t, public.Access{Reason: public.ReasonNotPublic}, access)
	embedded, err := h.service.CheckFormAccess(anonymous(t), public.CheckFormAccessQuery{Slug: *private.Slug, Embed: true})
	require.NoError(t, err)
	require.True(t, embedded.Accessible, "an embed needs no public URL")
}

// The submission reads the access policy from the locked row, so a policy
// change committed while it waited for the lock decides it.
func TestSubmitDecidesOnTheLockedAccessPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *harness, *form.Form)
		code   connect.Code
	}{
		{name: "unpublished", change: func(_ *testing.T, _ *harness, value *form.Form) { value.Status = form.StatusDraft }, code: connect.CodeFailedPrecondition},
		{name: "opening moved to the future", change: func(_ *testing.T, _ *harness, value *form.Form) { value.OpensAt = new(time.Now().Add(time.Hour)) }, code: connect.CodeFailedPrecondition},
		{name: "closing moved to the past", change: func(_ *testing.T, _ *harness, value *form.Form) { value.ClosesAt = new(time.Now().Add(-time.Hour)) }, code: connect.CodeFailedPrecondition},
		{name: "password added", change: func(t *testing.T, h *harness, value *form.Form) { value.AccessPassword = h.hash(t, "new-password") }, code: connect.CodePermissionDenied},
		{name: "authentication required", change: func(_ *testing.T, _ *harness, value *form.Form) { value.RequireAuth = new(true) }, code: connect.CodeUnauthenticated},
		{name: "role restriction added", change: func(_ *testing.T, _ *harness, value *form.Form) { value.AllowedRoles = []string{"admin"} }, code: connect.CodeUnauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			stored := h.seedForm(t, func(value *form.Form) { test.change(t, h, value) })

			_, err := h.service.SubmitForm(anonymous(t), public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{"email":"a@example.com"}`)})

			requireCode(t, err, test.code)
			require.Empty(t, h.store.StoredSubmissions(stored.ID))
			require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
		})
	}
}

func TestSubmitAuditsAnonymousAndMemberSubmissionsWithoutAnswers(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)

	anonymousID, err := h.service.SubmitForm(anonymous(t), public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{"email":"private@example.com"}`), ClientAddress: "203.0.113.9"})
	require.NoError(t, err)
	memberCtx, memberID := member(t)
	memberSubmissionID, err := h.service.SubmitForm(memberCtx, public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{}`)})
	require.NoError(t, err)

	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.ActorKindAnonymous, records[0].Kind)
	require.Equal(t, anonymousID, records[0].TargetID)
	require.Equal(t, stored.ID, records[0].ParentID)
	require.Equal(t, sharedtelemetry.ActorKindMember, records[1].Kind)
	require.Equal(t, memberID, records[1].MemberID)
	require.Equal(t, memberSubmissionID, records[1].TargetID)
	for _, record := range records {
		require.Equal(t, sharedtelemetry.AuditFormSubmissionCreated, record.Action)
		require.Empty(t, record.ChangedFields)
	}
	submissions := h.store.StoredSubmissions(stored.ID)
	require.Len(t, submissions, 2)
	require.Equal(t, &memberID, submissions[0].MemberID)
}

func TestSubmitRollsBackWhenItsAuditFails(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	h.audit.Err = errors.New("form submission audit unavailable")

	_, err := h.service.SubmitForm(anonymous(t), public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{}`)})

	require.ErrorIs(t, err, h.audit.Err)
	require.Empty(t, h.store.StoredSubmissions(stored.ID))
	require.Equal(t, mutation.StepRollback, h.units.Steps()[len(h.units.Steps())-1])
}

func TestSubmitFailsClosedWhenTheDuplicateLookupFails(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, func(value *form.Form) { value.AllowDuplicateSubmission = new(false) })
	lookupFailure := errors.New("injected duplicate lookup failure")
	h.store.Fail = map[string]error{"HasMemberSubmission": lookupFailure}
	ctx, _ := member(t)

	_, err := h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{}`)})

	require.ErrorIs(t, err, lookupFailure)
	requireCode(t, err, connect.CodeInternal)
	require.Empty(t, h.store.StoredSubmissions(stored.ID))
}

func TestSubmitRejectsADuplicateMemberSubmission(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, func(value *form.Form) { value.AllowDuplicateSubmission = new(false) })
	ctx, _ := member(t)
	_, err := h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{}`)})
	require.NoError(t, err)

	_, err = h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{}`)})

	requireCode(t, err, connect.CodeFailedPrecondition)
	require.Len(t, h.store.StoredSubmissions(stored.ID), 1)
}

func TestSubmitToAMissingFormIsNotFound(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.SubmitForm(anonymous(t), public.SubmitFormCommand{FormID: uuid.NewString(), Data: []byte(`{}`)})
	requireCode(t, err, connect.CodeNotFound)
}

func TestSubmitTakesTheMemberPreferredLocaleWithoutAcceptLanguage(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	ctx, memberID := member(t)
	h.store.PutPreferredLocale(memberID, "ja")

	_, err := h.service.SubmitForm(ctx, public.SubmitFormCommand{FormID: stored.ID, Data: []byte(`{}`)})

	require.NoError(t, err)
	require.JSONEq(t, `{"__meta.locale":"ja"}`, string(h.store.StoredSubmissions(stored.ID)[0].Data))
}

func TestCheckFormAccessServesTheRequestedLocale(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	localized := `{"id":"public-form-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","label":"이메일"}]}]}`
	h.store.PutLocale(stored.ID, "ko", formtest.StoredLocale{Title: new("문의 양식"), ContentJSON: []byte(localized)})
	h.store.PutOGDisposition(stored.ID, "ko", form.LocalizedOGReady)

	access, err := h.service.CheckFormAccess(anonymous(t), public.CheckFormAccessQuery{Slug: *stored.Slug, AcceptLanguage: "ko-KR"})

	require.NoError(t, err)
	require.True(t, access.Accessible)
	require.Equal(t, "문의 양식", access.Form.Document.Title)
	require.Equal(t, "ko", access.Form.Localization.DisplayedLocale)
	require.Equal(t, []string{"en", "ko"}, access.Form.Localization.AvailableLocales)
	require.True(t, access.Form.Localization.IsFallback, "the missing consent label falls back to the source")
	require.Contains(t, string(access.Form.Document.Schema), `"label":"Consent"`)

	h.store.PutOGDisposition(stored.ID, "ko", form.LocalizedOGUnavailable)
	access, err = h.service.CheckFormAccess(anonymous(t), public.CheckFormAccessQuery{Slug: *stored.Slug, AcceptLanguage: "ko-KR"})
	require.NoError(t, err)
	require.Equal(t, "en", access.Form.Localization.DisplayedLocale, "a locale without its own OG image serves the source")
	require.Equal(t, "Public Password Form", access.Form.Document.Title)
}

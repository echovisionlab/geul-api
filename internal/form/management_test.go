package form_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateFormStoresADraftWithItsSourceLocalePolicyAndAudit(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.store.PutPreferredLocale(admin.memberID, "ko")
	slug := "contact"

	created, err := h.forms.CreateForm(admin.context(t), connect.NewRequest(&managev1.CreateFormRequest{
		Title: " Contact ", Slug: &slug, Schema: []byte(sourceSchema), Password: new("secret"), AllowedRoles: []string{"USER", "user"},
		RequireAuth: new(true),
	}))

	require.NoError(t, err)
	message := created.Msg
	require.Equal(t, "Contact", message.Title, "the stored title is trimmed")
	require.Equal(t, "ko", message.SourceLocale, "the creator's preferred locale is the source")
	require.Equal(t, managev1.FormStatus_FORM_STATUS_DRAFT, message.Status)
	require.Equal(t, []string{"user"}, message.AllowedRoles)
	require.True(t, message.HasPassword)
	require.JSONEq(t, sourceSchema, string(message.Schema))
	stored, ok := h.store.Stored(message.Id)
	require.True(t, ok)
	require.NotEqual(t, "secret", *stored.AccessPassword, "the password is stored hashed")
	locale, ok := h.store.StoredLocale(message.Id, "ko")
	require.True(t, ok)
	require.Equal(t, "Contact", *locale.Title)
	require.Equal(t, "Contact\nEmail", *locale.ContentText)
	require.Equal(t, []string{"contact"}, h.store.LockedRoutes())
	require.Equal(t, []formtest.OGRequest{{FormID: message.Id, Locale: "ko", Reason: "form_created"}}, h.store.OGRequests())
	touch, err := policyv1.Form.TouchPolicy(message.Id)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditFormCreated, records[0].Action)
	require.Equal(t, admin.memberID, records[0].MemberID)
}

func TestCreateFormPicksTheSourceLocaleFromTheRequestThenTheSite(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	fromRequest, err := h.forms.CreateForm(admin.context(t), withLanguage(&managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema)}, "ja-JP,ja;q=0.9"))
	require.NoError(t, err)
	require.Equal(t, "ja", fromRequest.Msg.SourceLocale)

	h.store.Settings.DefaultLocale = "ko"
	fromSite, err := h.forms.CreateForm(admin.context(t), connect.NewRequest(&managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema)}))
	require.NoError(t, err)
	require.Equal(t, "ko", fromSite.Msg.SourceLocale)

	h.store.Fail = map[string]error{"RuntimeSettings": errors.New("settings unavailable"), "PreferredLocale": errors.New("member unavailable")}
	fallback, err := h.forms.CreateForm(admin.context(t), connect.NewRequest(&managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema)}))
	require.NoError(t, err)
	require.Equal(t, "en", fallback.Msg.SourceLocale, "unreadable settings fall back to the built-in default")
}

func withLanguage[T any](message *T, acceptLanguage string) *connect.Request[T] {
	request := connect.NewRequest(message)
	request.Header().Set("Accept-Language", acceptLanguage)
	return request
}

func TestCreateFormRejectsBeforeItWrites(t *testing.T) {
	taken := "taken"
	route := "route"
	for _, test := range []struct {
		name    string
		request *managev1.CreateFormRequest
		code    connect.Code
		message string
	}{
		{name: "blank title", request: &managev1.CreateFormRequest{Title: " ", Schema: []byte(sourceSchema)}, code: connect.CodeInvalidArgument, message: "title is required"},
		{name: "slug of another form", request: &managev1.CreateFormRequest{Title: "Form", Slug: &taken, Schema: []byte(sourceSchema)}, code: connect.CodeAlreadyExists, message: "form with slug \"taken\" already exists"},
		{name: "slug of another route", request: &managev1.CreateFormRequest{Title: "Form", Slug: &route, Schema: []byte(sourceSchema)}, code: connect.CodeAlreadyExists, message: "form with slug \"route\" already exists"},
		{name: "invalid schema", request: &managev1.CreateFormRequest{Title: "Form", Schema: []byte(`{"steps":"invalid"}`)}, code: connect.CodeInvalidArgument},
		{name: "public without slug", request: &managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema), IsPublic: new(true)}, code: connect.CodeInvalidArgument, message: "is_public: requires a slug"},
		{name: "roles without auth", request: &managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema), AllowedRoles: []string{"admin"}}, code: connect.CodeInvalidArgument, message: "allowed_roles: requires require_auth"},
		{name: "unknown role", request: &managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema), AllowedRoles: []string{"owner"}}, code: connect.CodeInvalidArgument, message: "allowed_roles: invalid role: owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedForm(t, func(value *form.Form) { value.Slug = &taken })
			h.store.PutRoute(route)

			_, err := h.forms.CreateForm(newPrincipal().context(t), connect.NewRequest(test.request))

			requireCode(t, err, test.code, test.message)
			require.Empty(t, h.units.Steps())
			require.Empty(t, h.audit.Records())
		})
	}
}

func TestCreateFormRequiresCreateAuthorityOfAnActivePrincipal(t *testing.T) {
	h := newHarnessWith(t, &authztest.Checker{})
	_, err := h.forms.CreateForm(newPrincipal().context(t), connect.NewRequest(&managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema)}))
	requireCode(t, err, connect.CodePermissionDenied, "")

	h = newHarness(t)
	inactive := newPrincipal()
	h.fence.Deactivate(inactive.identityID)
	_, err = h.forms.CreateForm(inactive.context(t), connect.NewRequest(&managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema)}))
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	require.Empty(t, h.checker.Applied())
}

func TestCreateFormRollsBackAndCompensatesWhenItsAuditFails(t *testing.T) {
	h := newHarness(t)
	h.audit.Err = errors.New("audit unavailable")

	_, err := h.forms.CreateForm(newPrincipal().context(t), connect.NewRequest(&managev1.CreateFormRequest{Title: "Form", Schema: []byte(sourceSchema)}))

	require.ErrorIs(t, err, h.audit.Err)
	require.Equal(t, mutation.StepRollback, lastStep(h.units))
	require.Empty(t, h.store.OGRequests())
	require.Empty(t, h.checker.Applied(), "the relationships come after the audit")
}

func TestUpdateFormWritesSettingsAndAuditsWhatChanged(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	admin := newPrincipal()
	slug, password, public := "audited-form", "audit-password", true

	updated, err := h.forms.UpdateForm(admin.context(t), connect.NewRequest(&managev1.UpdateFormRequest{
		Id: stored.ID, Slug: &slug, IsPublic: &public, Password: &password,
	}))
	require.NoError(t, err)
	require.Equal(t, slug, updated.Msg.GetSlug())
	require.True(t, updated.Msg.IsPublic)
	require.True(t, updated.Msg.HasPassword)
	require.Equal(t, []string{slug}, h.store.LockedRoutes())

	published := managev1.FormStatus_FORM_STATUS_PUBLISHED
	_, err = h.forms.UpdateForm(admin.context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: stored.ID, Status: &published}))
	require.NoError(t, err)
	// Repeating the values changes nothing and records nothing.
	_, err = h.forms.UpdateForm(admin.context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: stored.ID, Status: &published, Password: &password}))
	require.NoError(t, err)

	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, []string{"direct_public", "password", "slug"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditStateDraft, records[1].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStatePublished, records[1].NewState)
	for _, record := range records {
		require.Equal(t, sharedtelemetry.AuditFormUpdated, record.Action)
		require.Equal(t, admin.memberID, record.MemberID)
	}
	require.Equal(t, []formtest.OGRequest{{FormID: stored.ID, Locale: "en", AllLocales: true, Reason: "form_updated"}}, h.store.OGRequests(),
		"publishing regenerates every locale")
}

func TestUpdateFormSavesASourceTitleAsTheNextDocumentRevision(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	before := h.store.DocumentRevisionOf(stored.ContentDocumentID)
	schema := `{"id":"source-schema","steps":[{"id":"step-1","title":"Reach us","fields":[{"id":"field-email","key":"email","label":"Your email","type":"email","required":true}]}]}`

	updated, err := h.forms.UpdateForm(newPrincipal().context(t), connect.NewRequest(&managev1.UpdateFormRequest{
		Id: stored.ID, Title: new("Renamed"), Schema: []byte(schema),
	}))

	require.NoError(t, err)
	require.Equal(t, "Renamed", updated.Msg.Title)
	require.JSONEq(t, schema, string(updated.Msg.Schema))
	require.NotEqual(t, before, h.store.DocumentRevisionOf(stored.ContentDocumentID))
	source, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Reach us\nYour email", *source.ContentText)
	require.Equal(t, []formtest.OGRequest{{FormID: stored.ID, Locale: "en", Reason: "form_updated"}}, h.store.OGRequests())
	require.Empty(t, h.audit.Records(), "the source document is not a setting")
}

func TestUpdateFormRejections(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	missing := uuid.NewString()
	unknown := managev1.FormStatus(99)
	_, err := h.forms.UpdateForm(newPrincipal().context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: missing, IsPublic: new(true)}))
	requireCode(t, err, connect.CodeNotFound, "form not found: "+missing)
	_, err = h.forms.UpdateForm(newPrincipal().context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: stored.ID, Slug: new("slug"), ClearSlug: true}))
	requireCode(t, err, connect.CodeInvalidArgument, "slug: cannot set and clear slug in the same request")
	_, err = h.forms.UpdateForm(newPrincipal().context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: stored.ID, Status: &unknown}))
	requireCode(t, err, connect.CodeInvalidArgument, "status: must be draft or published")
	_, err = h.forms.UpdateForm(newPrincipal().context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: stored.ID, Title: new(" ")}))
	requireCode(t, err, connect.CodeInvalidArgument, "title is required")
	require.Empty(t, h.audit.Records())

	denied := newHarnessWith(t, &authztest.Checker{})
	other := denied.seedForm(t, nil)
	_, err = denied.forms.UpdateForm(newPrincipal().context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: other.ID, IsPublic: new(true)}))
	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, denied.units.Steps(),
		"the root is locked before authority is checked")

	inactive := newPrincipal()
	h.fence.Deactivate(inactive.identityID)
	_, err = h.forms.UpdateForm(inactive.context(t), connect.NewRequest(&managev1.UpdateFormRequest{Id: stored.ID, IsPublic: new(true)}))
	requireCode(t, err, connect.CodePermissionDenied, "no permission to access this form")
}

func TestDeleteFormRemovesItsLinksImagesOGAndPolicy(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	admin := newPrincipal()

	_, err := h.forms.DeleteForm(admin.context(t), connect.NewRequest(&managev1.DeleteFormRequest{Id: stored.ID}))

	require.NoError(t, err)
	_, exists := h.store.Stored(stored.ID)
	require.False(t, exists)
	require.Equal(t, []string{stored.ID}, h.store.ShareDeletes())
	require.Equal(t, []string{stored.ID}, h.store.OGCancels())
	require.Equal(t, []string{stored.ID}, h.store.Released())
	remove, err := policyv1.Form.DeletePolicy(stored.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{remove}}, h.checker.Applied())
	require.Len(t, h.audit.Records(), 1)
	require.Equal(t, sharedtelemetry.AuditFormDeleted, h.audit.Records()[0].Action)
}

func TestDeleteFormKeepsTheFormWhenOGCancellationFails(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	h.store.Fail = map[string]error{"CancelOG": errors.New("forced OG cancellation failure")}

	_, err := h.forms.DeleteForm(newPrincipal().context(t), connect.NewRequest(&managev1.DeleteFormRequest{Id: stored.ID}))

	require.ErrorIs(t, err, h.store.Fail["CancelOG"])
	_, exists := h.store.Stored(stored.ID)
	require.True(t, exists)
	require.Empty(t, h.store.ShareDeletes(), "the share link deletion rolled back")
	require.Empty(t, h.checker.Applied())
	require.Empty(t, h.audit.Records())
}

func TestFeaturedImageMutationsBindReleaseAndAudit(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	fileID := uuid.NewString()
	image := &form.Asset{ID: "image", URL: "https://cdn.example.com/image.webp", SHA256: []byte{1}}
	h.store.PutFile(fileID, image)
	admin := newPrincipal()

	set, err := h.forms.SetFormFeaturedImage(admin.context(t), connect.NewRequest(&managev1.SetFormFeaturedImageRequest{FormId: stored.ID, FileId: fileID}))
	require.NoError(t, err)
	require.Equal(t, &commonv1.AssetRef{AssetId: "image", Url: "https://cdn.example.com/image.webp", Sha256: []byte{1}}, set.Msg.ImageAsset)
	require.Equal(t, "og-run", set.Msg.GetOgGenerationRunId())
	require.Equal(t, []formtest.Binding{{FormID: stored.ID, FileID: fileID}}, h.store.Bindings())
	got, err := h.forms.GetForm(admin.context(t), connect.NewRequest(&managev1.GetFormRequest{Id: stored.ID}))
	require.NoError(t, err)
	require.Equal(t, "image", got.Msg.FeaturedImageAsset.GetAssetId())

	// Setting the same File again changes nothing worth recording.
	_, err = h.forms.SetFormFeaturedImage(admin.context(t), connect.NewRequest(&managev1.SetFormFeaturedImageRequest{FormId: stored.ID, FileId: fileID}))
	require.NoError(t, err)
	removed, err := h.forms.DeleteFormFeaturedImage(admin.context(t), connect.NewRequest(&managev1.DeleteFormFeaturedImageRequest{FormId: stored.ID}))
	require.NoError(t, err)
	require.True(t, removed.Msg.Success)
	require.Equal(t, "og-run", removed.Msg.GetOgGenerationRunId())
	// Removing a missing image changes nothing.
	_, err = h.forms.DeleteFormFeaturedImage(admin.context(t), connect.NewRequest(&managev1.DeleteFormFeaturedImageRequest{FormId: stored.ID}))
	require.NoError(t, err)

	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[0].CollectionOperation)
	require.Equal(t, fileID, records[0].FileID)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, records[1].CollectionOperation)
	require.Equal(t, []string{stored.ID}, h.store.Released())
	require.Equal(t, []formtest.OGRequest{
		{FormID: stored.ID, AllLocales: true, Reason: "form_featured_image_updated"},
		{FormID: stored.ID, AllLocales: true, Reason: "form_featured_image_updated"},
		{FormID: stored.ID, AllLocales: true, Reason: "form_featured_image_removed"},
	}, h.store.OGRequests())
}

func TestFeaturedImageMutationsRejectAMissingFileAndAnEditor(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	missing := uuid.NewString()
	_, err := h.forms.SetFormFeaturedImage(newPrincipal().context(t), connect.NewRequest(&managev1.SetFormFeaturedImageRequest{FormId: stored.ID, FileId: missing}))
	requireCode(t, err, connect.CodeNotFound, "file not found: "+missing)

	denied := newHarnessWith(t, (&authztest.Checker{}).Allow(policyv1.Form.View(uuid.NewString())))
	other := denied.seedForm(t, nil)
	_, err = denied.forms.SetFormFeaturedImage(newPrincipal().context(t), connect.NewRequest(&managev1.SetFormFeaturedImageRequest{FormId: other.ID, FileId: uuid.NewString()}))
	requireCode(t, err, connect.CodePermissionDenied, "")
	_, err = denied.forms.DeleteFormFeaturedImage(newPrincipal().context(t), connect.NewRequest(&managev1.DeleteFormFeaturedImageRequest{FormId: other.ID}))
	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Empty(t, denied.audit.Records())
}

func TestGetFormHidesAFormTheCallerMayNotView(t *testing.T) {
	h := newHarnessWith(t, &authztest.Checker{})
	stored := h.seedForm(t, func(value *form.Form) { value.Slug = new("hidden") })
	_, err := h.forms.GetForm(newPrincipal().context(t), connect.NewRequest(&managev1.GetFormRequest{Id: "hidden"}))
	requireCode(t, err, connect.CodeNotFound, "form not found: hidden")

	h.checker.Allow(policyv1.Form.View(stored.ID))
	got, err := h.forms.GetForm(newPrincipal().context(t), connect.NewRequest(&managev1.GetFormRequest{Id: "hidden"}))
	require.NoError(t, err)
	require.Equal(t, stored.ID, got.Msg.Id)
	require.Equal(t, "Contact", got.Msg.Title)
	require.Equal(t, "en", got.Msg.SourceLocale)
}

func TestListFormsAdminProjectsTitlesAndSubmissionCounts(t *testing.T) {
	h := newHarness(t)
	older := h.seedForm(t, nil)
	newer := h.seedForm(t, func(value *form.Form) { value.CreatedAt = time.Unix(1_700_200_000, 0).UTC() })
	h.store.PutLocale(newer.ID, "en", formtest.StoredLocale{Title: new(" "), ContentJSON: []byte(sourceSchema)})
	h.store.PutSubmission(form.Submission{FormID: older.ID, Data: []byte(`{}`)})

	listed, err := h.forms.ListFormsAdmin(newPrincipal().context(t), connect.NewRequest(&managev1.ListFormsAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 1},
	}))

	require.NoError(t, err)
	require.Equal(t, &commonv1.PaginationResponse{Total: 2, Limit: 1, HasMore: true}, listed.Msg.Pagination)
	require.Equal(t, newer.ID, listed.Msg.Forms[0].Id)
	require.Equal(t, form.DefaultTitle, listed.Msg.Forms[0].Title)

	listed, err = h.forms.ListFormsAdmin(newPrincipal().context(t), connect.NewRequest(&managev1.ListFormsAdminRequest{
		Pagination: &commonv1.PaginationRequest{Offset: 1},
	}))
	require.NoError(t, err)
	require.Equal(t, &commonv1.PaginationResponse{Total: 2, Limit: 20, Offset: 1}, listed.Msg.Pagination)
	require.Equal(t, "Contact", listed.Msg.Forms[0].Title)
	require.Equal(t, int32(1), listed.Msg.Forms[0].SubmissionCount)

	denied := newHarnessWith(t, &authztest.Checker{})
	_, err = denied.forms.ListFormsAdmin(newPrincipal().context(t), connect.NewRequest(&managev1.ListFormsAdminRequest{}))
	requireCode(t, err, connect.CodePermissionDenied, "")
}

func TestCheckFormSlugAvailableChecksFormsAndRoutes(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, func(value *form.Form) { value.Slug = new("taken") })
	h.store.PutRoute("route")
	ctx := newPrincipal().context(t)
	for slug, want := range map[string]bool{"taken": false, "route": false, "free": true} {
		available, err := h.forms.CheckFormSlugAvailable(ctx, connect.NewRequest(&managev1.CheckFormSlugAvailableRequest{Slug: slug}))
		require.NoError(t, err)
		require.Equal(t, want, available.Msg.Available, slug)
	}
	own, err := h.forms.CheckFormSlugAvailable(ctx, connect.NewRequest(&managev1.CheckFormSlugAvailableRequest{Slug: "taken", ExcludeId: &stored.ID}))
	require.NoError(t, err)
	require.True(t, own.Msg.Available, "a Form's own slug is free for it")

	missing := uuid.NewString()
	_, err = h.forms.CheckFormSlugAvailable(ctx, connect.NewRequest(&managev1.CheckFormSlugAvailableRequest{Slug: "free", ExcludeId: &missing}))
	requireCode(t, err, connect.CodeNotFound, "form not found: "+missing)
	_, err = h.forms.CheckFormSlugAvailable(ctx, connect.NewRequest(&managev1.CheckFormSlugAvailableRequest{Slug: "a/b"}))
	requireCode(t, err, connect.CodeInvalidArgument, "")
}

func TestListFormsAdminHasNoMoreFormsPastTheLastOffset(t *testing.T) {
	h := newHarness(t)
	h.seedForm(t, nil)

	listed, err := h.forms.ListFormsAdmin(newPrincipal().context(t), connect.NewRequest(&managev1.ListFormsAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 100, Offset: math.MaxInt32 - 10},
	}))

	require.NoError(t, err)
	require.Empty(t, listed.Msg.Forms)
	require.False(t, listed.Msg.Pagination.HasMore, "the offset and limit must not overflow into a next page")
}

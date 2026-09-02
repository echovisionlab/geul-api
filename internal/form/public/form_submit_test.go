package public

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestValidateFormSubmissionDataPayloadRejectsReservedSystemMetadata(t *testing.T) {
	t.Parallel()

	err := validateFormSubmissionDataPayload([]byte(`{"email":"hello@example.com","__meta.locale":"spoofed"}`))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, err.Code())
	assert.Contains(t, err.Message(), "__meta.locale")
}

func TestValidateFormSubmissionDataPayloadRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	err := validateFormSubmissionDataPayload([]byte(`{"email":`))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, err.Code())
	assert.Contains(t, err.Message(), "must be a JSON object")

	err = validateFormSubmissionDataPayload([]byte(`null`))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, err.Code())
	assert.Contains(t, err.Message(), "must be a JSON object")
}

func TestBuildFormSubmissionSystemMetadataUsesRequestAndRuntimeContext(t *testing.T) {
	t.Parallel()

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("2e664e94-ec6f-4e4a-a3ab-b9e1d68adb8f"),
		MemberID:      auth.MemberID("53d353cf-e573-45e4-a6a0-5a759d17f354"),
		SessionID:     auth.SessionID("85dd63dd-9067-4480-b57f-c3a5a8f4edbe"),
		Authenticated: true,
	})
	ctx = geoip.WithInfo(ctx, &geoip.Info{
		CountryCode: "KR",
		TimeZone:    "Asia/Seoul",
	})

	req := connect.NewRequest(&openv1.SubmitFormRequest{Data: []byte(`{"email":"hello@example.com"}`)})
	req.Header().Set("Accept-Language", "ko-KR,ko;q=0.9")
	req.Header().Set("X-Forwarded-For", "198.51.100.10, 10.0.0.2")
	req.Header().Set("User-Agent", "metadata-unit-agent")

	metadata := buildFormSubmissionSystemMetadata(ctx, req)

	require.NotNil(t, metadata.ipAddress)
	assert.Equal(t, "198.51.100.10", *metadata.ipAddress)
	require.NotNil(t, metadata.userAgent)
	assert.Equal(t, "metadata-unit-agent", *metadata.userAgent)
	require.NotNil(t, metadata.locale)
	assert.Equal(t, "ko", *metadata.locale)
	require.NotNil(t, metadata.countryCode)
	assert.Equal(t, "KR", *metadata.countryCode)
	require.NotNil(t, metadata.timeZone)
	assert.Equal(t, "Asia/Seoul", *metadata.timeZone)
}

func TestBuildFormSubmissionSystemMetadataDoesNotReadProfileFromAuthContext(t *testing.T) {
	t.Parallel()

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("6d0a30cf-45e4-459c-8db3-0fb948fe3aee"),
		MemberID:      auth.MemberID("fcdb4fc5-8082-49b0-8227-a814a9bc0ae8"),
		SessionID:     auth.SessionID("570808bf-e8ca-4e24-a668-051633838f30"),
		Authenticated: true,
	})

	metadata := buildFormSubmissionSystemMetadata(ctx, connect.NewRequest(&openv1.SubmitFormRequest{}))

	assert.Nil(t, metadata.locale)
	assert.Nil(t, metadata.countryCode)
	assert.Nil(t, metadata.timeZone)
}

func TestStringifyDashboardValueSupportsStoredScalarTypes(t *testing.T) {
	t.Parallel()

	value, ok := stringifyDashboardValue("hello")
	require.True(t, ok)
	assert.Equal(t, "hello", value)

	value, ok = stringifyDashboardValue(float64(42.5))
	require.True(t, ok)
	assert.Equal(t, "42.5", value)

	value, ok = stringifyDashboardValue(true)
	require.True(t, ok)
	assert.Equal(t, "true", value)

	_, ok = stringifyDashboardValue(42)
	assert.False(t, ok)
	_, ok = stringifyDashboardValue(nil)
	assert.False(t, ok)
}

func TestMapFormAccessReasonToErrorCodes(t *testing.T) {
	t.Parallel()

	require.NoError(t, mapFormAccessReasonToError(openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, false))

	cases := []struct {
		name            string
		reason          openv1.FormAccessReason
		draftAsNotFound bool
		code            connect.Code
	}{
		{
			name:   "not found",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
			code:   connect.CodeNotFound,
		},
		{
			name:            "draft hidden as not found",
			reason:          openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED,
			draftAsNotFound: true,
			code:            connect.CodeNotFound,
		},
		{
			name:   "draft explicit precondition",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED,
			code:   connect.CodeFailedPrecondition,
		},
		{
			name:   "not public",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC,
			code:   connect.CodeNotFound,
		},
		{
			name:   "auth required",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED,
			code:   connect.CodeUnauthenticated,
		},
		{
			name:   "role denied",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_ROLE_NOT_ALLOWED,
			code:   connect.CodePermissionDenied,
		},
		{
			name:   "password required",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_PASSWORD_REQUIRED,
			code:   connect.CodePermissionDenied,
		},
		{
			name:   "not yet open",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_YET_OPEN,
			code:   connect.CodeFailedPrecondition,
		},
		{
			name:   "closed",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_CLOSED,
			code:   connect.CodeFailedPrecondition,
		},
		{
			name:   "max submissions",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_MAX_SUBMISSIONS_REACHED,
			code:   connect.CodeFailedPrecondition,
		},
		{
			name:   "duplicate submission",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_ALREADY_SUBMITTED,
			code:   connect.CodeFailedPrecondition,
		},
		{
			name:   "unknown reason",
			reason: openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED,
			code:   connect.CodeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapFormAccessReasonToError(tc.reason, tc.draftAsNotFound)
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err))
		})
	}
}

func TestFormAccessNormalizationAndStatusMapping(t *testing.T) {
	t.Parallel()

	assert.Equal(t, openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL, normalizeFormAccessContext(openv1.FormAccessContext_FORM_ACCESS_CONTEXT_UNSPECIFIED))
	assert.Equal(t, openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL, normalizeFormAccessContext(openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL))
	assert.Equal(t, openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED, normalizeFormAccessContext(openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED))

	assert.Equal(t, openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM, normalizeFormAccessTarget(openv1.FormAccessTarget_FORM_ACCESS_TARGET_UNSPECIFIED))
	assert.Equal(t, openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM, normalizeFormAccessTarget(openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM))
	assert.Equal(t, openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD, normalizeFormAccessTarget(openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD))

	svc := &FormService{}
	assert.Equal(t, openv1.FormStatus_FORM_STATUS_DRAFT, svc.toProtoStatus(model.FormStatus(managev1.FormStatus_FORM_STATUS_DRAFT.String())))
	assert.Equal(t, openv1.FormStatus_FORM_STATUS_PUBLISHED, svc.toProtoStatus(model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String())))
	assert.Equal(t, openv1.FormStatus_FORM_STATUS_UNSPECIFIED, svc.toProtoStatus(model.FormStatus("FORM_STATUS_CORRUPT")))
}

func TestApplyLocalizedFormDocumentHandlesNilCanonicalAndFallbackContent(t *testing.T) {
	t.Parallel()

	applyLocalizedFormDocument(nil, localizedContentSelection{
		Title: new("ignored"),
	})

	sourceSchema := []byte(`{"id":"schema","steps":[{"id":"step","title":"Contact","fields":[{"id":"name","key":"name","label":"Name","type":"text"}]}]}`)
	sourceText := "Contact\nName"
	document := &formdomain.FormSourceDocument{
		Title:       "Source title",
		Schema:      sourceSchema,
		ContentText: &sourceText,
	}
	localizedTitle := "Localized title"
	localizedText := "Contact\nNombre"
	applyLocalizedFormDocument(document, localizedContentSelection{
		Title:           &localizedTitle,
		ContentJSON:     []byte(`{"id":"schema","steps":[{"id":"step","title":"Contacto","fields":[{"id":"name","label":"Nombre"}]}]}`),
		ContentText:     &localizedText,
		SourceLocale:    "en",
		DisplayedLocale: "es",
	})
	assert.Equal(t, localizedTitle, document.Title)
	assert.JSONEq(t, `{"id":"schema","steps":[{"id":"step","title":"Contacto","fields":[{"id":"name","key":"name","label":"Nombre","type":"text"}]}]}`, string(document.Schema))
	require.NotNil(t, document.ContentText)
	assert.Equal(t, "Contacto\nNombre", *document.ContentText)

	rawLocalizedSchema := []byte(`{not-json`)
	rawText := "Raw localized text"
	fallback := &formdomain.FormSourceDocument{
		Title:       "Source title",
		Schema:      sourceSchema,
		ContentText: &sourceText,
	}
	applyLocalizedFormDocument(fallback, localizedContentSelection{
		ContentJSON:     rawLocalizedSchema,
		ContentText:     &rawText,
		SourceLocale:    "en",
		DisplayedLocale: "de",
	})
	assert.Equal(t, rawLocalizedSchema, fallback.Schema)
	require.NotNil(t, fallback.ContentText)
	assert.Equal(t, rawText, *fallback.ContentText)
}

func TestVerifyFormPasswordSupportsArgon2idAndLegacyBcrypt(t *testing.T) {
	t.Parallel()

	hasher := crypto.NewPasswordHasher(&crypto.Argon2idParams{
		Memory:      64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  8,
		KeyLength:   16,
	})
	svc := &FormService{password: hasher}

	argonHash, err := hasher.Hash("correct")
	require.NoError(t, err)
	assert.True(t, svc.verifyFormPassword("correct", argonHash))
	assert.False(t, svc.verifyFormPassword("wrong", argonHash))
	assert.False(t, svc.verifyFormPassword("correct", "$argon2id$not-valid"))

	bcryptHash, err := bcrypt.GenerateFromPassword([]byte("legacy"), bcrypt.MinCost)
	require.NoError(t, err)
	assert.True(t, svc.verifyFormPassword("legacy", string(bcryptHash)))
	assert.False(t, svc.verifyFormPassword("wrong", string(bcryptHash)))
}

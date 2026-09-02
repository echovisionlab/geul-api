package emaildelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var (
	testEmailCourierIssuanceKey = []byte("test-email-courier-issuance-secret")
	testEmailCourierNow         = time.Date(2026, time.July, 31, 12, 30, 0, 0, time.UTC)
)

type stubEmailCourierPublisher struct {
	requests []*managev1.SendEmailEvent
	err      error
}

func (s *stubEmailCourierPublisher) PublishAuthEmail(
	_ context.Context,
	job *managev1.SendEmailEvent,
) error {
	s.requests = append(s.requests, job)
	return s.err
}

func newTestEmailCourierService(
	publisher EmailCourierPublisher,
	identityManager auth.IdentityManager,
) *EmailCourierService {
	service := NewEmailCourierService(
		publisher,
		identityManager,
		&testAuthIssuanceAuthority{key: testEmailCourierIssuanceKey},
		15*time.Minute,
	)
	service.now = func() time.Time { return testEmailCourierNow }
	return service
}

func TestNormalizeIdentityCourierTemplateType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		in            string
		want          email.EventKey
		wantSupported bool
	}{
		{name: "verification code valid remains supported", in: "verification_code_valid", want: email.EventVerificationCode, wantSupported: true},
		{name: "login code valid maps to canonical login event", in: "login_code_valid", want: email.EventLoginCode, wantSupported: true},
		{name: "registration code valid maps to canonical registration event", in: "registration_code_valid", want: email.EventRegistrationCode, wantSupported: true},
		{name: "plain verification canonical key is not raw ingress", in: "verification_code"},
		{name: "plain login canonical key is not raw ingress", in: "login_code"},
		{name: "plain registration canonical key is not raw ingress", in: "registration_code"},
		{name: "verification invalid anti-enumeration selector is unsupported", in: "verification_code_invalid"},
		{name: "whitespace-wrapped selector is not accepted", in: " login_code_valid "},
		{name: "unknown type is unsupported", in: "custom_template_type"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, supported := normalizeIdentityCourierTemplateType(tc.in)
			assert.Equal(t, tc.wantSupported, supported)
			assert.Equal(t, tc.want, got)
		})
	}
}

// MAIL-02: rejected identity-courier inputs never reach the durable queue.
func TestEmailCourierServiceSendEmailSkipsUnsupportedSelectorsWithoutQueueing(t *testing.T) {
	t.Parallel()

	for _, templateType := range []string{
		"verification_code_invalid",
		"verification_code",
		"login_code",
		"registration_code",
		"custom_template_type",
	} {
		t.Run(templateType, func(t *testing.T) {
			t.Parallel()

			enqueuer := &stubEmailCourierPublisher{}
			service := newTestEmailCourierService(enqueuer, nil)
			resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
				Recipient:    "user@example.com",
				TemplateType: templateType,
			}))

			require.NoError(t, err)
			require.False(t, resp.Msg.Queued)
			require.Empty(t, enqueuer.requests)
		})
	}
}

// MAIL-01: every accepted challenge becomes exactly one canonical queue event.
func TestEmailCourierServiceSendEmailBuildsExpectedQueueMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		templateType       string
		templateData       structured.Fields
		identityID         string
		wantQueuedType     string
		wantQueuedTemplate map[string]string
		wantVerification   bool
		wantLogin          bool
		wantRegistration   bool
		wantLocale         string
	}{
		{
			name:         "verification code carries account verification context",
			templateType: "verification_code_valid",
			templateData: structured.Fields{
				"verification_code":  "123456",
				"verification_url":   "https://example.com/verify",
				"request_url":        "https://example.com/verification",
				"expires_in_minutes": 10.0,
				"identity": structured.Fields{
					"id": "identity-2",
				},
			},
			identityID:     "identity-2",
			wantQueuedType: "verification_code",
			wantQueuedTemplate: map[string]string{
				"verification_code":  "123456",
				"verification_url":   "https://example.com/verify",
				"expires_in_minutes": "10",
			},
			wantVerification: true,
		},
		{
			name:         "login code uses existing identity context",
			templateType: "login_code_valid",
			templateData: structured.Fields{
				"login_code":             "789012",
				"request_url":            "https://example.com/login",
				"expires_in_minutes":     10.0,
				"ignored_nested_payload": structured.Fields{"ignored": true},
				"identity": structured.Fields{
					"id": "login-identity",
				},
			},
			identityID:     "login-identity",
			wantQueuedType: "login_code",
			wantQueuedTemplate: map[string]string{
				"login_code":         "789012",
				"expires_in_minutes": "10",
			},
			wantLogin: true,
		},
		{
			name:         "registration code uses pre-identity recipient context",
			templateType: "registration_code_valid",
			templateData: structured.Fields{
				"registration_code":  "345678",
				"request_url":        "https://example.com/registration",
				"expires_in_minutes": 10.0,
				"transient_payload": structured.Fields{
					"locale": "pt-PT",
				},
			},
			wantQueuedType: "registration_code",
			wantQueuedTemplate: map[string]string{
				"registration_code":  "345678",
				"expires_in_minutes": "10",
			},
			wantRegistration: true,
			wantLocale:       "pt-PT",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			enqueuer := &stubEmailCourierPublisher{}
			var identityManager auth.IdentityManager
			if tc.wantLogin {
				identityManager = &courierIdentityManager{
					identity: emailCourierLoginIdentity(tc.identityID, "user@example.com"),
				}
			}
			service := newTestEmailCourierService(enqueuer, identityManager)

			data := authCourierTemplateDataForTest(
				t,
				tc.templateType,
				"user@example.com",
				tc.name,
				testEmailCourierNow.Add(-time.Minute),
				tc.templateData,
			)

			resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
				Recipient:    "user@example.com",
				TemplateType: tc.templateType,
				TemplateData: data,
			}))
			require.NoError(t, err)
			require.True(t, resp.Msg.Queued)

			require.Len(t, enqueuer.requests, 1)
			job := enqueuer.requests[0]
			require.NotNil(t, job)

			assert.Equal(t, "user@example.com", job.Recipient)
			assert.Equal(t, tc.wantQueuedType, job.TemplateType)
			assert.Equal(t, tc.wantQueuedTemplate, job.TemplateData)
			require.NotEmpty(t, job.GetIssuanceId())
			require.NotNil(t, job.GetExpiresAt())
			expectedIdempotencyKey, err := authentication.AuthCodeIdempotencyKey(
				email.EventKey(tc.wantQueuedType),
				"user@example.com",
				job.GetIssuanceId(),
			)
			require.NoError(t, err)
			assert.Equal(t, expectedIdempotencyKey, job.GetMessageId())
			if tc.wantLocale == "" {
				assert.Nil(t, job.Locale)
			} else {
				require.NotNil(t, job.Locale)
				assert.Equal(t, tc.wantLocale, job.GetLocale())
			}
			if tc.wantVerification {
				verification := job.GetAccountVerification()
				require.NotNil(t, verification)
				assert.Equal(t, tc.identityID, verification.GetIdentityId())
				assert.Equal(t, "user@example.com", verification.GetTargetEmail())
			}
			if tc.wantLogin {
				login := job.GetAuthLogin()
				require.NotNil(t, login)
				assert.Equal(t, tc.identityID, login.GetIdentityId())
				assert.Equal(t, "user@example.com", login.GetTargetEmail())
			}
			if tc.wantRegistration {
				registration := job.GetAuthRegistration()
				require.NotNil(t, registration)
				assert.Equal(t, "user@example.com", registration.GetTargetEmail())
			}
		})
	}
}

func TestLocaleFromTemplateDataUsesSupportedProviderMetadataOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		data structured.Fields
		want string
	}{
		{
			name: "transient locale is canonicalized",
			data: structured.Fields{
				"transient_payload": structured.Fields{"locale": "zh-Hant"},
			},
			want: "zh-TW",
		},
		{
			name: "identity preferred locale is a fallback",
			data: structured.Fields{
				"identity": structured.Fields{
					"traits": structured.Fields{"preferred_locale": "ko_KR"},
				},
			},
			want: "ko",
		},
		{
			name: "unsupported locale is ignored",
			data: structured.Fields{
				"transient_payload": structured.Fields{"locale": "xx"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := structpb.NewStruct(tc.data)
			require.NoError(t, err)
			assert.Equal(t, tc.want, localeFromTemplateData(data))
		})
	}
}

func TestEmailCourierServiceSendEmailAllowsUnverifiedUsableCodeAddress(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{}
	identity := emailCourierLoginIdentity("login-identity", "user@example.com")
	identity.VerifiableAddresses = []auth.VerifiableAddress{{
		Via:      "email",
		Value:    "user@example.com",
		Verified: false,
	}}
	service := newTestEmailCourierService(enqueuer, &courierIdentityManager{identity: identity})
	data := authCourierTemplateDataForTest(
		t,
		"login_code_valid",
		"user@example.com",
		"unverified-login-address",
		testEmailCourierNow.Add(-time.Minute),
		structured.Fields{
			"login_code": "123456",
			"identity":   structured.Fields{"id": identity.ID},
		},
	)

	resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
		Recipient:    "user@example.com",
		TemplateType: "login_code_valid",
		TemplateData: data,
	}))

	require.NoError(t, err)
	require.True(t, resp.Msg.Queued)
	require.Len(t, enqueuer.requests, 1)
}

func TestEmailCourierServiceSendEmailRejectsMismatchedCodeAddress(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{}
	identity := emailCourierLoginIdentity("login-identity", "other@example.com")
	service := newTestEmailCourierService(enqueuer, &courierIdentityManager{identity: identity})
	data := authCourierTemplateDataForTest(
		t,
		"login_code_valid",
		"user@example.com",
		"mismatched-login-address",
		testEmailCourierNow.Add(-time.Minute),
		structured.Fields{
			"login_code": "123456",
			"identity":   structured.Fields{"id": identity.ID},
		},
	)

	resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
		Recipient:    "user@example.com",
		TemplateType: "login_code_valid",
		TemplateData: data,
	}))

	require.NoError(t, err)
	require.False(t, resp.Msg.Queued)
	require.Empty(t, enqueuer.requests)
}

func TestEmailCourierServiceSendEmailRejectsUnavailableLoginIdentity(t *testing.T) {
	t.Parallel()

	banned := emailCourierLoginIdentity("login-banned", "user@example.com")
	banned.MetadataAdmin = structured.Fields{"banned": true}
	inactive := emailCourierLoginIdentity("login-inactive", "user@example.com")
	inactive.State = auth.KratosStateInactive

	for _, tc := range []struct {
		name     string
		identity *auth.Identity
	}{
		{name: "banned identity", identity: banned},
		{name: "inactive identity", identity: inactive},
		{name: "missing identity manager"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			enqueuer := &stubEmailCourierPublisher{}
			var manager auth.IdentityManager
			identityID := "login-missing"
			if tc.identity != nil {
				manager = &courierIdentityManager{identity: tc.identity}
				identityID = tc.identity.ID
			}
			service := newTestEmailCourierService(enqueuer, manager)
			data := authCourierTemplateDataForTest(
				t,
				"login_code_valid",
				"user@example.com",
				"unavailable-login-"+tc.name,
				testEmailCourierNow.Add(-time.Minute),
				structured.Fields{
					"login_code": "123456",
					"identity":   structured.Fields{"id": identityID},
				},
			)

			resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
				Recipient:    "user@example.com",
				TemplateType: "login_code_valid",
				TemplateData: data,
			}))

			require.NoError(t, err)
			require.False(t, resp.Msg.Queued)
			require.Empty(t, enqueuer.requests)
		})
	}
}

func TestEmailCourierServiceSendEmailRejectsInvalidPasswordlessContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		templateType string
	}{
		{name: "verification without identity", templateType: "verification_code_valid"},
		{name: "login without identity", templateType: "login_code_valid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enqueuer := &stubEmailCourierPublisher{}
			service := newTestEmailCourierService(enqueuer, nil)
			data := authCourierTemplateDataForTest(
				t,
				tc.templateType,
				"user@example.com",
				"invalid-context-"+tc.templateType,
				testEmailCourierNow.Add(-time.Minute),
				structured.Fields{"login_code": "123456"},
			)

			resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
				Recipient:    "user@example.com",
				TemplateType: tc.templateType,
				TemplateData: data,
			}))

			require.NoError(t, err)
			require.False(t, resp.Msg.Queued)
			require.Empty(t, enqueuer.requests)
		})
	}
}

// A failed durable enqueue is reported so the identity courier can retry the
// same generated code without losing the mail intent.
func TestEmailCourierServiceSendEmailRetriesFailedDurableEnqueue(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{
		err: errors.New("broker unavailable"),
	}
	service := newTestEmailCourierService(enqueuer, nil)
	data := authCourierTemplateDataForTest(
		t,
		"registration_code_valid",
		"new@example.com",
		"failed-enqueue-retry",
		testEmailCourierNow.Add(-time.Minute),
		structured.Fields{"registration_code": "123456"},
	)
	request := func() *connect.Request[intrav1.SendEmailRequest] {
		return connect.NewRequest(&intrav1.SendEmailRequest{
			Recipient:    "new@example.com",
			TemplateType: "registration_code_valid",
			TemplateData: data,
		})
	}

	_, err := service.SendEmail(context.Background(), request())
	require.Error(t, err)
	require.Len(t, enqueuer.requests, 1)

	enqueuer.err = nil
	response, err := service.SendEmail(context.Background(), request())
	require.NoError(t, err)
	require.True(t, response.Msg.Queued)
	require.Len(t, enqueuer.requests, 2)
	require.Equal(t, enqueuer.requests[0].GetMessageId(), enqueuer.requests[1].GetMessageId())
}

// Exact courier retries retain the same issuance and durable idempotency key.
// A newly reserved issuance stays distinct even when Kratos reuses the same
// short code value.
func TestEmailCourierServiceSendEmailUsesExactIssuanceIdempotency(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{}
	service := newTestEmailCourierService(enqueuer, nil)
	request := func(code string, issuanceSeed string) *connect.Request[intrav1.SendEmailRequest] {
		data := authCourierTemplateDataForTest(
			t,
			"registration_code_valid",
			"new@example.com",
			issuanceSeed,
			testEmailCourierNow.Add(-time.Minute),
			structured.Fields{"registration_code": code},
		)
		return connect.NewRequest(&intrav1.SendEmailRequest{
			Recipient:    "new@example.com",
			TemplateType: "registration_code_valid",
			TemplateData: data,
		})
	}

	first, err := service.SendEmail(context.Background(), request("123456", "issuance-a"))
	require.NoError(t, err)
	require.True(t, first.Msg.Queued)
	duplicate, err := service.SendEmail(context.Background(), request("123456", "issuance-a"))
	require.NoError(t, err)
	require.True(t, duplicate.Msg.Queued)
	third, err := service.SendEmail(context.Background(), request("123456", "issuance-b"))
	require.NoError(t, err)
	require.True(t, third.Msg.Queued)

	require.Len(t, enqueuer.requests, 3)
	require.Equal(t, enqueuer.requests[0].GetMessageId(), enqueuer.requests[1].GetMessageId())
	require.NotEqual(t, enqueuer.requests[0].GetMessageId(), enqueuer.requests[2].GetMessageId())
}

func TestEmailCourierServiceSendEmailFailsClosedWithoutGeneratedCode(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{}
	service := newTestEmailCourierService(enqueuer, nil)
	data := authCourierTemplateDataForTest(
		t,
		"registration_code_valid",
		"new@example.com",
		"missing-code",
		testEmailCourierNow.Add(-time.Minute),
		structured.Fields{"expires_in_minutes": 15},
	)

	_, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
		Recipient:    "new@example.com",
		TemplateType: "registration_code_valid",
		TemplateData: data,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, enqueuer.requests)
}

func TestEmailCourierServiceSendEmailFailsClosedOnInvalidIssuanceProvenance(t *testing.T) {
	t.Parallel()

	valid := authCourierTemplateDataForTest(
		t,
		"registration_code_valid",
		"new@example.com",
		"forged-provenance",
		testEmailCourierNow.Add(-time.Minute),
		structured.Fields{"registration_code": "123456"},
	)
	forged, err := structpb.NewStruct(valid.AsMap())
	require.NoError(t, err)
	forged.Fields["transient_payload"].GetStructValue().
		Fields[authentication.AuthCodeIssuanceProvenanceNamespace].GetStructValue().
		Fields["mac"] = structpb.NewStringValue(strings.Repeat("0", 64))
	malformed, err := structpb.NewStruct(structured.Fields{
		"registration_code": "123456",
		"transient_payload": structured.Fields{
			authentication.AuthCodeIssuanceProvenanceNamespace: "not-an-object",
		},
	})
	require.NoError(t, err)
	missing, err := structpb.NewStruct(structured.Fields{
		"registration_code": "123456",
		"transient_payload": structured.Fields{"locale": "ko"},
	})
	require.NoError(t, err)

	for name, data := range map[string]*structpb.Struct{
		"missing":   missing,
		"forged":    forged,
		"malformed": malformed,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			enqueuer := &stubEmailCourierPublisher{}
			service := newTestEmailCourierService(enqueuer, nil)
			_, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
				Recipient:    "new@example.com",
				TemplateType: "registration_code_valid",
				TemplateData: data,
			}))
			require.Error(t, err)
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			require.Empty(t, enqueuer.requests)
		})
	}
}

func TestEmailCourierServiceExpiryUsesReservedIssuanceTimeAndShortestLifespan(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{}
	service := newTestEmailCourierService(enqueuer, nil)
	issuedAt := testEmailCourierNow.Add(-5 * time.Minute)
	data := authCourierTemplateDataForTest(
		t,
		"registration_code_valid",
		"new@example.com",
		"delayed-courier",
		issuedAt,
		structured.Fields{
			"registration_code":  "123456",
			"expires_in_minutes": 10,
		},
	)

	response, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
		Recipient:    "new@example.com",
		TemplateType: "registration_code_valid",
		TemplateData: data,
	}))
	require.NoError(t, err)
	require.True(t, response.Msg.Queued)
	require.Len(t, enqueuer.requests, 1)
	require.Equal(t, issuedAt.Add(10*time.Minute), enqueuer.requests[0].GetExpiresAt().AsTime())

	expiredData := authCourierTemplateDataForTest(
		t,
		"registration_code_valid",
		"expired@example.com",
		"expired-courier",
		testEmailCourierNow.Add(-20*time.Minute),
		structured.Fields{
			"registration_code":  "654321",
			"expires_in_minutes": 10,
		},
	)
	_, err = service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
		Recipient:    "expired@example.com",
		TemplateType: "registration_code_valid",
		TemplateData: expiredData,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Len(t, enqueuer.requests, 1)
}

func TestNewEmailCourierServiceRequiresDependencies(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		NewEmailCourierService(nil, nil, &testAuthIssuanceAuthority{key: []byte("secret")}, time.Minute)
	})
	require.Panics(t, func() {
		NewEmailCourierService(&stubEmailCourierPublisher{}, nil, nil, time.Minute)
	})
	require.Panics(t, func() {
		NewEmailCourierService(&stubEmailCourierPublisher{}, nil, &testAuthIssuanceAuthority{key: []byte("secret")}, 0)
	})
}

func TestEmailCourierServiceSendEmailSkipsVerificationWithoutIdentityContext(t *testing.T) {
	t.Parallel()

	enqueuer := &stubEmailCourierPublisher{}
	service := newTestEmailCourierService(enqueuer, nil)
	data := authCourierTemplateDataForTest(
		t,
		"verification_code_valid",
		"user@example.com",
		"verification-without-identity",
		testEmailCourierNow.Add(-time.Minute),
		structured.Fields{"verification_code": "123456"},
	)

	resp, err := service.SendEmail(context.Background(), connect.NewRequest(&intrav1.SendEmailRequest{
		Recipient:    "user@example.com",
		TemplateType: "verification_code_valid",
		TemplateData: data,
	}))
	require.NoError(t, err)
	require.False(t, resp.Msg.Queued)
	require.Empty(t, enqueuer.requests)
}

func authCourierTemplateDataForTest(
	t *testing.T,
	templateType string,
	recipient string,
	issuanceSeed string,
	issuedAt time.Time,
	data structured.Fields,
) *structpb.Struct {
	t.Helper()
	eventKey, supported := normalizeIdentityCourierTemplateType(templateType)
	require.True(t, supported)
	issuanceDigest := sha256.Sum256([]byte(issuanceSeed))
	issuanceID := hex.EncodeToString(issuanceDigest[:])
	provenance, err := authentication.NewAuthCodeIssuanceProvenance(
		testEmailCourierIssuanceKey,
		eventKey,
		recipient,
		issuanceID,
		issuedAt,
	)
	require.NoError(t, err)

	copyData := make(structured.Fields, len(data)+1)
	maps.Copy(copyData, data)
	transient := make(structured.Fields)
	if existing, ok := copyData["transient_payload"].(structured.Fields); ok {
		maps.Copy(transient, existing)
	}
	transient[authentication.AuthCodeIssuanceProvenanceNamespace] = structured.Fields{
		"version":     provenance.Version,
		"issuance_id": provenance.IssuanceID,
		"issued_at":   provenance.IssuedAt,
		"purpose":     provenance.Purpose,
		"recipient":   provenance.Recipient,
		"mac":         provenance.MAC,
	}
	copyData["transient_payload"] = transient
	result, err := structpb.NewStruct(copyData)
	require.NoError(t, err)
	return result
}

func emailCourierLoginIdentity(identityID, email string) *auth.Identity {
	return &auth.Identity{
		ID:     identityID,
		State:  auth.KratosStateActive,
		Traits: structured.Fields{"email": email},
		Credentials: map[string]auth.Credential{
			"code": {
				Type:        "code",
				Identifiers: []string{email},
			},
		},
	}
}

type courierIdentityManager struct {
	identity *auth.Identity
}

type testAuthIssuanceAuthority struct {
	key []byte
}

func (a *testAuthIssuanceAuthority) Verify(
	eventKey email.EventKey,
	recipient string,
	provenance AuthIssuanceProvenance,
) (AuthIssuance, error) {
	issuedAt, err := authentication.VerifyAuthCodeIssuanceProvenance(
		a.key,
		eventKey,
		recipient,
		authentication.AuthCodeIssuanceProvenance{
			Version: provenance.Version, IssuanceID: provenance.IssuanceID,
			IssuedAt: provenance.IssuedAt, Purpose: provenance.Purpose,
			Recipient: provenance.Recipient, MAC: provenance.MAC,
		},
	)
	if err != nil {
		return AuthIssuance{}, err
	}
	return AuthIssuance{IssuanceID: provenance.IssuanceID, IssuedAt: issuedAt}, nil
}

func (*testAuthIssuanceAuthority) RestoreSettingsVerification(
	context.Context,
	email.EventKey,
	string,
	string,
) (AuthIssuance, bool, error) {
	return AuthIssuance{}, false, nil
}

func (*testAuthIssuanceAuthority) IdempotencyKey(
	eventKey email.EventKey,
	recipient string,
	issuanceID string,
) (string, error) {
	return authentication.AuthCodeIdempotencyKey(eventKey, recipient, issuanceID)
}

func (m *courierIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != identityID {
		return nil, errors.New("identity not found")
	}
	return m.identity, nil
}

func (m *courierIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (*courierIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}

func (*courierIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}

func (*courierIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}

func (*courierIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}

func (*courierIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (*courierIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (*courierIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }

func (m *courierIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if m.identity == nil {
		return "", nil
	}
	return m.identity.CurrentEmail(), nil
}

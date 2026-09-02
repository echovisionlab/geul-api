//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/account"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/handler"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// MAIL-01: pinned Kratos login, registration, and verification triggers each
// reach one provider-neutral queue event through the authenticated ingress.
func TestPinnedKratosCodeFlowsReachAuthenticatedCourierQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	hookServer, hookBaseURL, err := startHookServer()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hookServer.Close()) })

	stack, err := testutil.StartBackendIntegrationStackWithOptions(
		ctx,
		testutil.BackendIntegrationStackOptions{HookBaseURL: hookBaseURL},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	var issuanceLimiter *authentication.AuthCodeIssuanceLimiter
	stack.KratosPublicURL, issuanceLimiter = serveBackendIntegrationKratosProxy(t, stack)
	publisher, err := mq.NewPublisher(stack.Postgres.SQLDB)
	require.NoError(t, err)
	hookPublisher := &controlledHookEmailPublisher{delegate: publisher}
	telemetryWriter := apitelemetry.NewDurableWriter(stack.Postgres.DB)
	directRoles := accountadapter.AccountDirectRoleTransition{}
	accountEmailChangeLifecycle := account.NewAuditedAccountEmailChangeLifecycle(
		stack.Postgres.DB,
		stack.KratosClient,
		hookPublisher,
		accountadapter.MemberEmailProjection{},
		telemetryWriter,
	)
	_, rawCourierHandler := intrav1connect.NewEmailCourierServiceHandler(
		emaildelivery.NewEmailCourierService(
			publisher,
			stack.KratosClient,
			emaildeliveryadapter.NewAuthIssuanceAuthority(
				[]byte(testutil.IntegrationTokenSigningSecret),
				issuanceLimiter,
				accountEmailChangeLifecycle,
			),
			15*time.Minute,
		),
	)
	go accountEmailChangeLifecycle.Start(ctx)
	registrationMembers := member.NewMemberProvisioner(
		stack.Postgres.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		memberadapter.AccountEmailProjection{},
		directRoles,
	)
	loginHooks := authentication.NewLoginHookService(
		stack.KratosClient,
		authenticationadapter.NewLoginMemberProvisioner(registrationMembers),
		authentication.NewAuthBootstrapService(
			stack.Postgres.DB,
			stack.SpiceDBClient,
			telemetryWriter,
			directRoles,
		),
	)
	registrationHooks := authentication.NewRegistrationHookPolicy(
		authenticationadapter.NewRegistrationReuseHoldChecker(stack.Postgres.DB),
	)
	accountSettingsHooks := account.NewAccountSettingsHookService(
		stack.KratosClient,
		accountEmailChangeLifecycle,
	)
	credentialHooks := account.NewAccountCredentialHookLifecycle(
		stack.Postgres.DB,
		stack.KratosClient,
		telemetryWriter,
		hookPublisher,
		accountadapter.MemberEmailProjection{},
	)
	hooksHandler := handler.NewHooksHandler(
		loginHooks,
		registrationHooks,
		accountSettingsHooks,
		credentialHooks,
	)
	hookServer.SetHandlers(
		hooksHandler,
		protectBackendIntegrationCourier(
			stack.TokenSigningSecret,
			rawCourierHandler,
		),
	)
	purgeBackendIntegrationEmailQueue(t, stack.Postgres.SQLDB)
	t.Cleanup(func() {
		purgeBackendIntegrationEmailQueue(t, stack.Postgres.SQLDB)
	})

	rejectedRegistrationEmail := fmt.Sprintf(
		"registration-rejected-%d@example.test",
		time.Now().UnixNano(),
	)
	rejectedRegistrationFlow := startNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		"registration",
		"",
	)
	rejectedRegistrationStart := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		rejectedRegistrationFlow.Action,
		"",
		structured.Fields{
			"method":            "code",
			"transient_payload": structured.Fields{"preferred_locale": "en"},
			"traits": structured.Fields{
				"email":         rejectedRegistrationEmail,
				"pending_email": "pending-" + rejectedRegistrationEmail,
			},
		},
	)
	require.Equal(
		t,
		http.StatusBadRequest,
		rejectedRegistrationStart.StatusCode,
		rejectedRegistrationStart.Body,
	)

	rejectedRegistrationEmailEvent := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventRegistrationCode.String(),
	)
	require.Equal(
		t,
		rejectedRegistrationEmail,
		rejectedRegistrationEmailEvent.GetRecipient(),
	)
	rejectedRegistrationCode := strings.TrimSpace(
		rejectedRegistrationEmailEvent.GetTemplateData()["registration_code"],
	)
	require.NotEmpty(t, rejectedRegistrationCode)
	rejectedRegistrationCompletion := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		rejectedRegistrationFlow.Action,
		"",
		structured.Fields{
			"code":              rejectedRegistrationCode,
			"method":            "code",
			"transient_payload": structured.Fields{"preferred_locale": "en"},
			"traits": structured.Fields{
				"email":         rejectedRegistrationEmail,
				"pending_email": "pending-" + rejectedRegistrationEmail,
			},
		},
	)
	require.NotEqual(
		t,
		http.StatusOK,
		rejectedRegistrationCompletion.StatusCode,
		rejectedRegistrationCompletion.Body,
	)
	require.Contains(
		t,
		rejectedRegistrationCompletion.Body,
		"registration_pending_email_forbidden",
	)
	var rejectedIdentityCount int64
	require.NoError(t, stack.Postgres.DB.Raw(`
		SELECT COUNT(*)
		FROM kratos.identities
		WHERE LOWER(BTRIM(traits ->> 'email')) = LOWER(BTRIM(?))
	`, rejectedRegistrationEmail).Scan(&rejectedIdentityCount).Error)
	require.Zero(t, rejectedIdentityCount)
	requireNoBackendIntegrationEmailEventForRecipient(
		t,
		stack.Postgres.SQLDB,
		rejectedRegistrationEmail,
		500*time.Millisecond,
	)

	registrationEmail := fmt.Sprintf("registration-%d@example.test", time.Now().UnixNano())
	registrationFlow := startNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		"registration",
		"",
	)
	registrationStart := submitNativeAPIFlow(t, stack.KratosPublicURL, registrationFlow.Action, "", structured.Fields{
		"method":            "code",
		"transient_payload": structured.Fields{"preferred_locale": "en"},
		"traits": structured.Fields{
			"email": registrationEmail,
		},
	})
	require.Equal(t, http.StatusBadRequest, registrationStart.StatusCode, registrationStart.Body)
	assertPinnedKratosRegistrationCodeFlowSurface(t, registrationStart.Body, "info")
	registration := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventRegistrationCode.String(),
	)
	require.Equal(t, email.EventRegistrationCode.String(), registration.GetTemplateType())
	require.Equal(t, registrationEmail, registration.GetRecipient())
	require.NotNil(t, registration.GetAuthRegistration())
	registrationCode := strings.TrimSpace(registration.GetTemplateData()["registration_code"])
	require.NotEmpty(t, registrationCode)
	invalidRegistrationCompletion := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		registrationFlow.Action,
		"",
		structured.Fields{
			"code":              "000000",
			"method":            "code",
			"transient_payload": structured.Fields{"preferred_locale": "en"},
			"traits": structured.Fields{
				"email": registrationEmail,
			},
		},
	)
	require.Equal(
		t,
		http.StatusBadRequest,
		invalidRegistrationCompletion.StatusCode,
		invalidRegistrationCompletion.Body,
	)
	assertPinnedKratosRegistrationCodeFlowSurface(t, invalidRegistrationCompletion.Body, "error")
	registrationCompletion := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		registrationFlow.Action,
		"",
		structured.Fields{
			"code":              registrationCode,
			"method":            "code",
			"transient_payload": structured.Fields{"preferred_locale": "en"},
			"traits": structured.Fields{
				"email": registrationEmail,
			},
		},
	)
	require.Equal(t, http.StatusOK, registrationCompletion.StatusCode, registrationCompletion.Body)
	var registeredSession struct {
		SessionToken string `json:"session_token"`
		Session      struct {
			Identity auth.Identity `json:"identity"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal([]byte(registrationCompletion.Body), &registeredSession))
	require.NotEmpty(t, registeredSession.SessionToken)
	require.NotEmpty(t, registeredSession.Session.Identity.ID)
	var provisionedMember struct {
		ID        string `gorm:"column:id"`
		Nickname  string `gorm:"column:nickname"`
		Onboarded bool   `gorm:"column:onboarded"`
	}
	require.NoError(t, stack.Postgres.DB.Raw(`
		SELECT id::text AS id, nickname, onboarded
		FROM member
		WHERE account_identity_id = ?::uuid
	`, registeredSession.Session.Identity.ID).Scan(&provisionedMember).Error)
	require.NotEmpty(t, provisionedMember.ID)
	require.Equal(t, provisionedMember.ID, provisionedMember.Nickname)
	require.False(t, provisionedMember.Onboarded)
	requireNoBackendIntegrationEmailEventForRecipient(
		t,
		stack.Postgres.SQLDB,
		registrationEmail,
		500*time.Millisecond,
	)
	onboardingNickname := fmt.Sprintf("RegisteredMember%d", time.Now().UnixNano())
	memberService := member.NewAuditedMemberService(
		stack.Postgres.DB,
		"",
		stack.SpiceDBClient,
		stack.KratosClient,
		backendIntegrationNoopFileDeleter{},
		"https://www.example.test",
		publisher,
		apitelemetry.NewDurableWriter(stack.Postgres.DB),
		member.WithAccountSummaryReader(memberadapter.AccountSummaryReader{}),
		member.WithAccountEmailProjection(memberadapter.AccountEmailProjection{}),
	)
	onboardingRequestContext, err := sharedtelemetry.NewPublicRequestContext("127.0.0.1")
	require.NoError(t, err)
	onboardingResponse, err := memberService.CompleteMyOnboarding(
		auth.WithUser(sharedtelemetry.WithRequestContext(ctx, onboardingRequestContext), &auth.UserInfo{
			IdentityID:    auth.IdentityID(registeredSession.Session.Identity.ID),
			MemberID:      auth.MemberID(provisionedMember.ID),
			Authenticated: true,
		}),
		connect.NewRequest(&managev1.CompleteMyOnboardingRequest{Nickname: onboardingNickname}),
	)
	require.NoError(t, err)
	require.True(t, onboardingResponse.Msg.GetOnboarded())
	require.Equal(t, onboardingNickname, onboardingResponse.Msg.GetMember().GetNickname())
	welcome := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventWelcome.String(),
	)
	require.Equal(t, registrationEmail, welcome.GetRecipient())
	require.Equal(t, "welcome:"+registeredSession.Session.Identity.ID, welcome.GetMessageId())
	require.Equal(t, onboardingNickname, welcome.GetTemplateData()["name"])
	var onboardingAuditCount int64
	require.NoError(t, stack.Postgres.DB.Table("public.domain_audit").
		Where("action = ? AND target_id = ? AND attributes->>'nickname' = ?", sharedtelemetry.AuditMemberUpdated, provisionedMember.ID, onboardingNickname).
		Count(&onboardingAuditCount).Error)
	require.Equal(t, int64(1), onboardingAuditCount)

	loginEmail := fmt.Sprintf("login-%d@example.test", time.Now().UnixNano())
	loginIdentity, err := stack.KratosClient.CreateIdentity(ctx, &auth.Identity{
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits: structured.Fields{
			"email": loginEmail,
		},
	})
	require.NoError(t, err)
	testutil.SeedKratosVerifiableEmailFixture(t, stack.Postgres.DB, loginIdentity.ID, loginEmail, true, time.Now().UTC())
	submitNativeCodeRequest(t, stack.KratosPublicURL, "login", structured.Fields{
		"csrf_token": "",
		"identifier": loginEmail,
		"method":     "code",
	})
	login := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventLoginCode.String(),
	)
	require.Equal(t, email.EventLoginCode.String(), login.GetTemplateType())
	require.Equal(t, loginEmail, login.GetRecipient())
	require.Equal(t, loginIdentity.ID, login.GetAuthLogin().GetIdentityId())

	verificationEmail := fmt.Sprintf("verification-%d@example.test", time.Now().UnixNano())
	verificationIdentity, err := stack.KratosClient.CreateIdentity(ctx, &auth.Identity{
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits: structured.Fields{
			"email": verificationEmail,
		},
	})
	require.NoError(t, err)
	submitNativeCodeRequest(t, stack.KratosPublicURL, "verification", structured.Fields{
		"email":  verificationEmail,
		"method": "code",
	})
	verification := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventVerificationCode.String(),
	)
	require.Equal(t, email.EventVerificationCode.String(), verification.GetTemplateType())
	require.Equal(t, verificationEmail, verification.GetRecipient())
	require.Equal(
		t,
		verificationIdentity.ID,
		verification.GetAccountVerification().GetIdentityId(),
	)

	assertPinnedKratosEmailChangeFlow(
		t,
		stack,
		registeredSession.SessionToken,
		registeredSession.Session.Identity.ID,
		registrationEmail,
		hookPublisher,
		accountEmailChangeLifecycle,
	)
}

type backendIntegrationNoopFileDeleter struct{}

func (backendIntegrationNoopFileDeleter) DeleteFileByID(context.Context, string) error {
	return nil
}

func serveBackendIntegrationKratosProxy(
	t *testing.T,
	stack *testutil.BackendIntegrationStack,
) (string, *authentication.AuthCodeIssuanceLimiter) {
	t.Helper()
	limiter := authentication.NewAuthCodeIssuanceLimiter(
		stack.Postgres.DB,
		[]byte(testutil.IntegrationTokenSigningSecret),
		authentication.AuthCodeIssuanceLimits{
			Cooldown:        time.Millisecond,
			AddressHourly:   100,
			IPTenMinute:     1000,
			GlobalPerMinute: 1000,
		},
	)
	proxy, err := authentication.NewKratosPublicProxy(
		stack.KratosPublicURL,
		limiter,
		[]byte(testutil.IntegrationTokenSigningSecret),
	)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server.URL, limiter
}

type controlledHookEmailPublisher struct {
	delegate *mq.Publisher
	fail     atomic.Bool
}

func (p *controlledHookEmailPublisher) EnqueueProtobuf(
	ctx context.Context,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if p.fail.Load() {
		return fmt.Errorf("controlled hook publisher failure")
	}
	return p.delegate.EnqueueProtobuf(ctx, queue, messageID, message)
}

func (p *controlledHookEmailPublisher) NotifyProtobuf(
	ctx context.Context,
	signal string,
	message proto.Message,
) error {
	return p.delegate.NotifyProtobuf(ctx, signal, message)
}

func (p *controlledHookEmailPublisher) PublishSendEmail(
	ctx context.Context,
	event *managev1.SendEmailEvent,
) error {
	if p.fail.Load() {
		return fmt.Errorf("controlled hook publisher failure")
	}
	return p.delegate.PublishSendEmail(ctx, event)
}

type nativeAPIFlow struct {
	ID     string
	Action string
}

type nativeAPISubmission struct {
	StatusCode int
	Body       string
}

func submitNativeCodeRequest(
	t *testing.T,
	publicURL string,
	flowType string,
	payload structured.Fields,
) {
	t.Helper()

	flow := startNativeAPIFlow(t, publicURL, flowType, "")
	submission := submitNativeAPIFlow(t, publicURL, flow.Action, "", payload)
	require.Contains(
		t,
		[]int{http.StatusOK, http.StatusBadRequest},
		submission.StatusCode,
		submission.Body,
	)
}

func startNativeAPIFlow(
	t *testing.T,
	publicURL string,
	flowType string,
	sessionToken string,
) nativeAPIFlow {
	t.Helper()

	client := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequest(
		http.MethodGet,
		publicURL+"/self-service/"+flowType+"/api",
		nil,
	)
	require.NoError(t, err)
	if sessionToken != "" {
		request.Header.Set("X-Session-Token", sessionToken)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))

	var flow struct {
		ID string `json:"id"`
		UI struct {
			Action string `json:"action"`
		} `json:"ui"`
	}
	require.NoError(t, json.Unmarshal(body, &flow))
	action, err := url.Parse(flow.UI.Action)
	require.NoError(t, err)
	public, err := url.Parse(publicURL)
	require.NoError(t, err)
	action.Scheme = public.Scheme
	action.Host = public.Host
	return nativeAPIFlow{
		ID:     flow.ID,
		Action: action.String(),
	}
}

func submitNativeAPIFlow(
	t *testing.T,
	_ string,
	action string,
	sessionToken string,
	payload structured.Fields,
) nativeAPISubmission {
	t.Helper()

	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)
	request, err := http.NewRequest(
		http.MethodPost,
		action,
		bytes.NewReader(payloadBytes),
	)
	require.NoError(t, err)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if sessionToken != "" {
		request.Header.Set("X-Session-Token", sessionToken)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return nativeAPISubmission{
		StatusCode: response.StatusCode,
		Body:       string(body),
	}
}

func assertPinnedKratosRegistrationCodeFlowSurface(t *testing.T, body, messageType string) {
	t.Helper()
	var flow struct {
		Active string `json:"active"`
		State  string `json:"state"`
		UI     struct {
			Messages []struct {
				Type string `json:"type"`
			} `json:"messages"`
			Nodes []struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"nodes"`
		} `json:"ui"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &flow))
	require.Equal(t, "code", flow.Active)
	require.Equal(t, "sent_email", flow.State)
	require.Len(t, flow.UI.Messages, 1)
	require.Equal(t, messageType, flow.UI.Messages[0].Type)
	require.Condition(t, func() bool {
		for _, node := range flow.UI.Nodes {
			if node.Attributes.Name == "code" {
				return true
			}
		}
		return false
	}, "Kratos registration code flow must expose the code node")
}

func assertPinnedKratosEmailChangeFlow(
	t *testing.T,
	stack *testutil.BackendIntegrationStack,
	sessionToken string,
	identityID string,
	currentEmail string,
	hookPublisher *controlledHookEmailPublisher,
	accountEmailChangeLifecycle *account.AccountEmailChangeLifecycle,
) {
	t.Helper()

	// AUTH-12: staging an address, abandoning it, or submitting an invalid
	// proof must not replace the canonical email or its code credential.
	nextEmail := fmt.Sprintf("changed-%d@example.test", time.Now().UnixNano())
	requireCodeCredentialIncludesAddress(t, stack.KratosClient, identityID, currentEmail)
	settingsFlow := startNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		"settings",
		sessionToken,
	)

	submission := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		settingsFlow.Action,
		sessionToken,
		structured.Fields{
			"method": "profile",
			"traits": structured.Fields{
				"email":         currentEmail,
				"pending_email": nextEmail,
			},
		},
	)
	require.Equal(t, http.StatusOK, submission.StatusCode, submission.Body)
	var settingsResult struct {
		Identity     auth.Identity `json:"identity"`
		ContinueWith []struct {
			Action string `json:"action"`
			Flow   struct {
				ID                string `json:"id"`
				VerifiableAddress string `json:"verifiable_address"`
			} `json:"flow"`
		} `json:"continue_with"`
	}
	require.NoError(t, json.Unmarshal([]byte(submission.Body), &settingsResult))
	require.Equal(t, identityID, settingsResult.Identity.ID)
	require.Equal(t, currentEmail, settingsResult.Identity.CurrentEmail())
	require.True(t, settingsResult.Identity.CurrentEmailVerified())
	require.Equal(t, nextEmail, *settingsResult.Identity.GetTraitString("pending_email"))
	require.Len(t, settingsResult.ContinueWith, 1)
	require.Equal(t, "show_verification_ui", settingsResult.ContinueWith[0].Action)
	require.NotEmpty(t, settingsResult.ContinueWith[0].Flow.ID)
	require.Equal(t, nextEmail, settingsResult.ContinueWith[0].Flow.VerifiableAddress)
	requireCodeCredentialIncludesAddress(t, stack.KratosClient, identityID, currentEmail)

	verification := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventVerificationCode.String(),
	)
	require.Equal(t, email.EventVerificationCode.String(), verification.GetTemplateType())
	require.Equal(t, nextEmail, verification.GetRecipient())
	require.Equal(t, identityID, verification.GetAccountVerification().GetIdentityId())
	verificationCode := strings.TrimSpace(verification.GetTemplateData()["verification_code"])
	require.NotEmpty(t, verificationCode)
	invalidVerificationCode := "000000"
	if verificationCode == invalidVerificationCode {
		invalidVerificationCode = "111111"
	}

	invalidProof := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		stack.KratosPublicURL+"/self-service/verification?flow="+
			url.QueryEscape(settingsResult.ContinueWith[0].Flow.ID),
		sessionToken,
		structured.Fields{
			"code":   invalidVerificationCode,
			"method": "code",
		},
	)
	require.Equal(t, http.StatusOK, invalidProof.StatusCode, invalidProof.Body)
	require.Contains(t, invalidProof.Body, "verification code is invalid")
	afterInvalid, err := stack.KratosClient.GetIdentity(context.Background(), identityID)
	require.NoError(t, err)
	require.Equal(t, currentEmail, afterInvalid.CurrentEmail())
	require.Equal(t, nextEmail, *afterInvalid.GetTraitString("pending_email"))
	requireCodeCredentialIncludesAddress(t, stack.KratosClient, identityID, currentEmail)
	requireNoBackendIntegrationEmailEventForRecipient(
		t,
		stack.Postgres.SQLDB,
		currentEmail,
		500*time.Millisecond,
	)

	hookPublisher.fail.Store(true)
	t.Cleanup(func() {
		hookPublisher.fail.Store(false)
	})
	verificationCompletion := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		stack.KratosPublicURL+"/self-service/verification?flow="+
			url.QueryEscape(settingsResult.ContinueWith[0].Flow.ID),
		sessionToken,
		structured.Fields{
			"code":   verificationCode,
			"method": "code",
		},
	)
	require.Equal(
		t,
		http.StatusOK,
		verificationCompletion.StatusCode,
		verificationCompletion.Body,
	)

	updated, err := stack.KratosClient.GetIdentity(context.Background(), identityID)
	require.NoError(t, err)
	require.Equal(t, nextEmail, updated.CurrentEmail())
	require.True(t, updated.CurrentEmailVerified())
	require.Nil(t, updated.GetTraitString("pending_email"))
	requireCodeCredentialIncludesAddress(t, stack.KratosClient, identityID, nextEmail)
	requireCodeCredentialExcludesAddress(t, stack.KratosClient, identityID, currentEmail)

	// AUTH-12: canonical application succeeds even while notification publish is
	// unavailable. The one active request remains and reconstructs the same
	// stable command when reconciliation is retried after publisher recovery.
	var requestID string
	require.Eventually(t, func() bool {
		err := stack.Postgres.DB.Raw(`
			SELECT id::text
			FROM account_email_change_request
			WHERE identity_id = ?::uuid
			  AND LOWER(BTRIM(requested_email_address)) = LOWER(BTRIM(?))
			ORDER BY created_at DESC
			LIMIT 1
		`, identityID, nextEmail).Scan(&requestID).Error
		return err == nil && strings.TrimSpace(requestID) != ""
	}, 10*time.Second, 100*time.Millisecond)
	hookPublisher.fail.Store(false)
	require.NoError(t, accountEmailChangeLifecycle.ReconcileRequest(t.Context(), requestID))

	notification := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventPrimaryEmailChanged.String(),
	)
	require.Equal(t, currentEmail, notification.GetRecipient())
	require.Equal(t, currentEmail, notification.GetTemplateData()["old_email"])
	require.Equal(t, nextEmail, notification.GetTemplateData()["new_email"])
	require.Equal(t, "account-email-change:"+requestID, notification.GetMessageId())
	require.Eventually(t, func() bool {
		var count int64
		err := stack.Postgres.DB.Raw(`
			SELECT COUNT(*)
			FROM account_email_change_request
			WHERE id = ?::uuid
		`, requestID).Scan(&count).Error
		return err == nil && count == 0
	}, 10*time.Second, 100*time.Millisecond)

	// AUTH-12: Kratos reserves the staged verifiable address before application,
	// so another identity cannot claim it between code issuance and proof.
	occupiedEmail := fmt.Sprintf("occupied-%d@example.test", time.Now().UnixNano())
	collisionFlow := startNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		"settings",
		sessionToken,
	)
	collision := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		collisionFlow.Action,
		sessionToken,
		structured.Fields{
			"method": "profile",
			"traits": structured.Fields{
				"email":         nextEmail,
				"pending_email": occupiedEmail,
			},
		},
	)
	require.Equal(t, http.StatusOK, collision.StatusCode, collision.Body)
	var collisionResult struct {
		ContinueWith []struct {
			Action string `json:"action"`
			Flow   struct {
				ID                string `json:"id"`
				VerifiableAddress string `json:"verifiable_address"`
			} `json:"flow"`
		} `json:"continue_with"`
	}
	require.NoError(t, json.Unmarshal([]byte(collision.Body), &collisionResult))
	require.Len(t, collisionResult.ContinueWith, 1)
	require.Equal(t, "show_verification_ui", collisionResult.ContinueWith[0].Action)
	require.Equal(t, occupiedEmail, collisionResult.ContinueWith[0].Flow.VerifiableAddress)

	collisionVerification := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventVerificationCode.String(),
	)
	require.Equal(t, occupiedEmail, collisionVerification.GetRecipient())
	collisionCode := strings.TrimSpace(
		collisionVerification.GetTemplateData()["verification_code"],
	)
	require.NotEmpty(t, collisionCode)

	_, err = stack.KratosClient.CreateIdentity(context.Background(), &auth.Identity{
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits: structured.Fields{
			"email": occupiedEmail,
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "kratos returned 409")

	collisionCompletion := submitNativeAPIFlow(
		t,
		stack.KratosPublicURL,
		stack.KratosPublicURL+"/self-service/verification?flow="+
			url.QueryEscape(collisionResult.ContinueWith[0].Flow.ID),
		sessionToken,
		structured.Fields{
			"code":   collisionCode,
			"method": "code",
		},
	)
	require.Equal(t, http.StatusOK, collisionCompletion.StatusCode, collisionCompletion.Body)

	afterCollision, err := stack.KratosClient.GetIdentity(context.Background(), identityID)
	require.NoError(t, err)
	require.Equal(t, occupiedEmail, afterCollision.CurrentEmail())
	require.True(t, afterCollision.CurrentEmailVerified())
	require.Nil(t, afterCollision.GetTraitString("pending_email"))
	requireCodeCredentialIncludesAddress(t, stack.KratosClient, identityID, occupiedEmail)
	requireCodeCredentialExcludesAddress(t, stack.KratosClient, identityID, nextEmail)

	collisionNotification := requireBackendIntegrationEmailEvent(
		t,
		stack.Postgres.SQLDB,
		email.EventPrimaryEmailChanged.String(),
	)
	require.Equal(t, nextEmail, collisionNotification.GetRecipient())
	require.Equal(t, nextEmail, collisionNotification.GetTemplateData()["old_email"])
	require.Equal(
		t,
		occupiedEmail,
		collisionNotification.GetTemplateData()["new_email"],
	)
	require.NotEmpty(t, collisionNotification.GetMessageId())
	requireNoBackendIntegrationEmailEventForRecipient(
		t,
		stack.Postgres.SQLDB,
		nextEmail,
		500*time.Millisecond,
	)
}

func requireCodeCredentialIncludesAddress(
	t *testing.T,
	client auth.IdentityGetter,
	identityID string,
	expectedEmail string,
) {
	t.Helper()

	identity, err := client.GetIdentityWithIncludeCredential(
		context.Background(),
		identityID,
		"code",
	)
	require.NoError(t, err)
	require.NotNil(t, identity)
	credential, ok := identity.Credentials["code"]
	require.True(t, ok, "code credential missing: %#v", identity.Credentials)
	normalizedExpected := strings.ToLower(strings.TrimSpace(expectedEmail))
	require.Contains(t, credential.Identifiers, normalizedExpected)
}

func requireCodeCredentialExcludesAddress(
	t *testing.T,
	client auth.IdentityGetter,
	identityID string,
	unexpectedEmail string,
) {
	t.Helper()

	identity, err := client.GetIdentityWithIncludeCredential(
		context.Background(),
		identityID,
		"code",
	)
	require.NoError(t, err)
	require.NotNil(t, identity)
	credential, ok := identity.Credentials["code"]
	require.True(t, ok, "code credential missing: %#v", identity.Credentials)
	normalizedUnexpected := strings.ToLower(strings.TrimSpace(unexpectedEmail))
	require.NotContains(t, credential.Identifiers, normalizedUnexpected)
}

func requireBackendIntegrationEmailEvent(
	t *testing.T,
	db *sql.DB,
	expectedTemplateType string,
) *managev1.SendEmailEvent {
	t.Helper()

	var event *managev1.SendEmailEvent
	queueName := backendIntegrationEmailQueue(expectedTemplateType)
	require.Eventually(t, func() bool {
		messages, err := testutil.ReadPGMQ(t.Context(), db, queueName, time.Minute, 1)
		if err != nil || len(messages) == 0 {
			return false
		}
		message := messages[0]
		body, err := message.Envelope.Payload()
		if err != nil {
			return false
		}
		var candidate managev1.SendEmailEvent
		if err := proto.Unmarshal(body, &candidate); err != nil {
			return false
		}
		require.NoError(t, testutil.CompletePGMQ(t.Context(), db, queueName, message.TransportID))
		if candidate.GetTemplateType() != expectedTemplateType {
			return false
		}
		event = &candidate
		return true
	}, 10*time.Second, 50*time.Millisecond)
	return event
}

func purgeBackendIntegrationEmailQueue(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, queueName := range []string{eventpkg.QueueEmailAuth, eventpkg.QueueEmailSend} {
		require.NoError(t, testutil.PurgePGMQQueue(ctx, db, queueName))
	}
}

func backendIntegrationEmailQueue(templateType string) string {
	switch emailauthoring.ClassifyEmailTemplateType(templateType) {
	case emailauthoring.EmailTemplateClassAccountVerification,
		emailauthoring.EmailTemplateClassAuthLogin,
		emailauthoring.EmailTemplateClassAuthRegistration:
		return eventpkg.QueueEmailAuth
	default:
		return eventpkg.QueueEmailSend
	}
}

func requireNoBackendIntegrationEmailEventForRecipient(
	t *testing.T,
	db *sql.DB,
	recipient string,
	duration time.Duration,
) {
	t.Helper()

	require.Never(t, func() bool {
		for _, queueName := range []string{eventpkg.QueueEmailAuth, eventpkg.QueueEmailSend} {
			messages, err := testutil.ReadPGMQ(t.Context(), db, queueName, time.Minute, 1)
			if err != nil || len(messages) == 0 {
				continue
			}
			message := messages[0]
			body, err := message.Envelope.Payload()
			require.NoError(t, err)
			require.NoError(t, testutil.CompletePGMQ(t.Context(), db, queueName, message.TransportID))
			var event managev1.SendEmailEvent
			require.NoError(t, proto.Unmarshal(body, &event))
			if strings.EqualFold(
				strings.TrimSpace(event.GetRecipient()),
				strings.TrimSpace(recipient),
			) {
				return true
			}
		}
		return false
	}, duration, 50*time.Millisecond)
}

//go:build integration

package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

var (
	telemetryIntegrationPostgres        *testutil.AppPostgres
	telemetryIntegrationPostgresOnce    sync.Once
	telemetryIntegrationPostgresCleanup func() error
	telemetryIntegrationPostgresErr     error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if telemetryIntegrationPostgresCleanup != nil {
		if err := telemetryIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup telemetry integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func telemetryIntegrationStack(t *testing.T) *testutil.AppPostgres {
	t.Helper()

	telemetryIntegrationPostgresOnce.Do(func() {
		telemetryIntegrationPostgres, telemetryIntegrationPostgresCleanup, telemetryIntegrationPostgresErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
		if telemetryIntegrationPostgresErr != nil {
			fmt.Fprintf(os.Stderr, "start telemetry integration postgres: %v\n", telemetryIntegrationPostgresErr)
		}
	})
	require.NoError(t, telemetryIntegrationPostgresErr)
	require.NoError(t, telemetryIntegrationPostgres.DB.Exec(`
		TRUNCATE TABLE public.domain_audit, public.security_access
	`).Error)
	return telemetryIntegrationPostgres
}

func TestDurableWriterPersistsDomainAndSecurityRecords(t *testing.T) {
	stack := telemetryIntegrationStack(t)
	memberID := uuid.NewString()
	require.NoError(t, stack.DB.Exec(`
		INSERT INTO public.member (id, nickname, onboarded, primary_email, available_emails)
		VALUES (?, 'audit-member', true, 'actor@example.test', ARRAY['actor@example.test']::text[])
	`, memberID).Error)

	requestID := uuid.NewString()
	memberActor := sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: memberID}
	postVersionActor := sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindSystem, Service: string(sharedtelemetry.ServiceEditorCollab)}
	contributorIDs := []string{memberID}
	audit, err := sharedtelemetry.NewPostVersionCreatedAuditRecord(sharedtelemetry.AuditMetadata{
		AuditID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: requestID}, RecordActor: postVersionActor,
	}, uuid.NewString(), uuid.NewString(), contributorIDs)
	require.NoError(t, err)

	writer := apitelemetry.NewDurableWriter(stack.DB)
	require.NoError(t, stack.DB.Transaction(func(transaction *gorm.DB) error {
		return writer.AppendDomainAuditInTransaction(context.Background(), transaction, audit)
	}))

	var storedAudit struct {
		Action               string
		ActorKind            string
		ActorMemberID        *string
		ActorService         *string
		RequestID            string
		TargetType           string
		TargetID             string
		ChangedFields        pq.StringArray `gorm:"type:text[]"`
		VersionID            string
		ContributorMemberIDs pq.StringArray `gorm:"type:uuid[]"`
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id, actor_service,
		       request_id::text AS request_id,
		       target_type, target_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'version_id' AS version_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'contributor_member_ids')) AS contributor_member_ids
		FROM public.domain_audit WHERE audit_id = ?
	`, audit.AuditID).Scan(&storedAudit).Error)
	require.Equal(t, string(sharedtelemetry.AuditPostUpdated), storedAudit.Action)
	require.Equal(t, string(sharedtelemetry.ActorKindSystem), storedAudit.ActorKind)
	require.Nil(t, storedAudit.ActorMemberID)
	require.NotNil(t, storedAudit.ActorService)
	require.Equal(t, string(sharedtelemetry.ServiceEditorCollab), *storedAudit.ActorService)
	require.Equal(t, requestID, storedAudit.RequestID)
	require.Equal(t, "post", storedAudit.TargetType)
	require.Equal(t, audit.TargetID, storedAudit.TargetID)
	require.Equal(t, pq.StringArray{"version"}, storedAudit.ChangedFields)
	require.Equal(t, audit.VersionID, storedAudit.VersionID)
	require.Equal(t, pq.StringArray(contributorIDs), storedAudit.ContributorMemberIDs)

	access, err := sharedtelemetry.NewAuthenticationSucceededRecord(sharedtelemetry.SecurityAccessMetadata{
		AccessID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: requestID}, RecordActor: memberActor, SourceIP: "192.0.2.23",
	}, sharedtelemetry.AuthenticationContext{
		FlowKind: sharedtelemetry.AuthenticationFlowLogin, AuthenticationMethod: sharedtelemetry.AuthenticationMethodOIDC,
		PrincipalState: sharedtelemetry.AuthenticationPrincipalActive, Provider: "google",
	})
	require.NoError(t, err)
	require.NoError(t, writer.AppendSecurityAccess(context.Background(), access))

	var storedAccess struct {
		Action               string
		ActorMemberID        string
		RequestID            string
		SourceIP             string
		FlowKind             string
		AuthenticationMethod string
		PrincipalState       string
		Provider             string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id, request_id::text AS request_id,
		       host(source_ip) AS source_ip,
		       attributes->>'flow_kind' AS flow_kind,
		       attributes->>'authentication_method' AS authentication_method,
		       attributes->>'principal_state' AS principal_state,
		       attributes->>'provider' AS provider
		FROM public.security_access WHERE access_id = ?
	`, access.AccessID).Scan(&storedAccess).Error)
	require.Equal(t, string(sharedtelemetry.SecurityAuthenticationSucceeded), storedAccess.Action)
	require.Equal(t, memberID, storedAccess.ActorMemberID)
	require.Equal(t, requestID, storedAccess.RequestID)
	require.Equal(t, "192.0.2.23", storedAccess.SourceIP)
	require.Equal(t, "login", storedAccess.FlowKind)
	require.Equal(t, "oidc", storedAccess.AuthenticationMethod)
	require.Equal(t, "active", storedAccess.PrincipalState)
	require.Equal(t, "google", storedAccess.Provider)

	unonboardedMemberID := uuid.NewString()
	require.NoError(t, stack.DB.Exec(`
		INSERT INTO public.member (id, nickname, onboarded, primary_email, available_emails)
		VALUES (?, 'onboarding-only', false, 'onboarding@example.test', ARRAY['onboarding@example.test']::text[])
	`, unonboardedMemberID).Error)
	onboardingAccess, err := sharedtelemetry.NewAuthenticationSucceededRecord(sharedtelemetry.SecurityAccessMetadata{
		AccessID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: uuid.NewString()},
		RecordActor: sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: unonboardedMemberID},
		SourceIP:    "192.0.2.24",
	}, sharedtelemetry.AuthenticationContext{
		FlowKind: sharedtelemetry.AuthenticationFlowRegistration, AuthenticationMethod: sharedtelemetry.AuthenticationMethodEmailCode,
		PrincipalState: sharedtelemetry.AuthenticationPrincipalOnboardingOnly,
	})
	require.NoError(t, err)
	require.NoError(t, writer.AppendSecurityAccess(context.Background(), onboardingAccess))
	require.NoError(t, stack.DB.Exec(`DELETE FROM public.member WHERE id = ?::uuid`, unonboardedMemberID).Error)

	var persistedOnboardingAccess struct {
		ActorMemberID  string
		PrincipalState string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT actor_member_id::text AS actor_member_id, attributes->>'principal_state' AS principal_state
		FROM public.security_access
		WHERE access_id = ?::uuid
	`, onboardingAccess.AccessID).Take(&persistedOnboardingAccess).Error)
	require.Equal(t, unonboardedMemberID, persistedOnboardingAccess.ActorMemberID)
	require.Equal(t, "onboarding_only", persistedOnboardingAccess.PrincipalState)

}

func TestDurableWriterRollsBackWithOwningDomainTransaction(t *testing.T) {
	stack := telemetryIntegrationStack(t)
	memberID := uuid.NewString()
	require.NoError(t, stack.DB.Exec(`
		INSERT INTO public.member (id, nickname, primary_email, available_emails)
		VALUES (?, 'rollback-member', 'rollback@example.test', ARRAY['rollback@example.test']::text[])
	`, memberID).Error)

	actor := sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: memberID}
	audit, err := sharedtelemetry.NewWorkCreatedAuditRecord(sharedtelemetry.AuditMetadata{
		AuditID: uuid.NewString(), OccurredAt: time.Now().UTC(), RecordActor: actor,
	}, uuid.NewString())
	require.NoError(t, err)
	writer := apitelemetry.NewDurableWriter(stack.DB)

	err = stack.DB.Transaction(func(transaction *gorm.DB) error {
		require.NoError(t, writer.AppendDomainAuditInTransaction(context.Background(), transaction, audit))
		return errors.New("force owning transaction rollback")
	})
	require.ErrorContains(t, err, "force owning transaction rollback")

	var count int64
	require.NoError(t, stack.DB.Table("public.domain_audit").Where("audit_id = ?", audit.AuditID).Count(&count).Error)
	require.Zero(t, count)
}

func TestDurableWriterMakesMatchingDomainAuditRetryIdempotent(t *testing.T) {
	stack := telemetryIntegrationStack(t)
	memberID := uuid.NewString()
	recordID := uuid.NewString()
	require.NoError(t, stack.DB.Exec(`
		INSERT INTO public.member (id, nickname, onboarded, primary_email, available_emails)
		VALUES (?, 'retry-member', true, 'retry@example.test', ARRAY['retry@example.test']::text[])
	`, memberID).Error)
	actor := sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: memberID}
	build := func(requestID, subject string) sharedtelemetry.AuditRecord {
		record, err := sharedtelemetry.NewAccountSocialLoginAddedAuditRecord(sharedtelemetry.AuditMetadata{
			AuditID: recordID, OccurredAt: time.Now().UTC(),
			Correlation: sharedtelemetry.Correlation{RequestID: requestID}, RecordActor: actor,
		}, memberID, "google", subject)
		require.NoError(t, err)
		return record
	}
	writer := apitelemetry.NewDurableWriter(stack.DB)
	appendRecord := func(record sharedtelemetry.AuditRecord) error {
		return stack.DB.Transaction(func(tx *gorm.DB) error {
			return writer.AppendDomainAuditInTransaction(t.Context(), tx, record)
		})
	}

	require.NoError(t, appendRecord(build(uuid.NewString(), "subject-1")))
	require.NoError(t, appendRecord(build(uuid.NewString(), "subject-1")))
	require.ErrorIs(t, appendRecord(build(uuid.NewString(), "subject-2")), apitelemetry.ErrAuditRecordIDCollision)

	var count int64
	require.NoError(t, stack.DB.Table("public.domain_audit").Where("audit_id = ?", recordID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestDurableWriterRejectsDuplicateRecordID(t *testing.T) {
	stack := telemetryIntegrationStack(t)
	requestID := uuid.NewString()
	access, err := sharedtelemetry.NewAuthenticationBlockedRecord(sharedtelemetry.SecurityAccessMetadata{
		AccessID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: requestID},
		RecordActor: sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindAnonymous}, SourceIP: "2001:db8::1",
	}, sharedtelemetry.AuthenticationContext{}, sharedtelemetry.AuthenticationBlockRateLimited)
	require.NoError(t, err)

	writer := apitelemetry.NewDurableWriter(stack.DB)
	require.NoError(t, writer.AppendSecurityAccess(context.Background(), access))
	require.Error(t, writer.AppendSecurityAccess(context.Background(), access))

	var count int64
	require.NoError(t, stack.DB.Table("public.security_access").Where("access_id = ?", access.AccessID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestAccessInterceptorPersistsOneTerminalAuthorizationDenialIntegration(t *testing.T) {
	stack := telemetryIntegrationStack(t)
	writer := apitelemetry.NewDurableWriter(stack.DB)
	access := apitelemetry.NewAccessLogInterceptor(writer)
	handler := connect.NewUnaryHandler(
		"/test.v1.PrivateService/GetSecret",
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
		},
		connect.WithInterceptors(access),
	)
	mux := http.NewServeMux()
	mux.Handle("/test.v1.PrivateService/", handler)
	request := httptest.NewRequest(http.MethodPost, "/test.v1.PrivateService/GetSecret", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	apitelemetry.NewHTTPHandler(mux, nil).ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)

	requestID := response.Header().Get(apitelemetry.RequestIDHeader)
	var stored struct {
		Action          string
		ActorKind       string
		RequestID       string
		SourceIP        string
		Reason          string
		AttemptedAction string
		Permission      string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_kind, request_id::text AS request_id, host(source_ip) AS source_ip,
		       attributes->>'reason' AS reason,
		       attributes->>'attempted_action' AS attempted_action,
		       attributes->>'permission' AS permission
		FROM public.security_access
		WHERE request_id = ?::uuid AND action = 'authorization.denied'
	`, requestID).Take(&stored).Error)
	require.Equal(t, string(sharedtelemetry.SecurityAuthorizationDenied), stored.Action)
	require.Equal(t, string(sharedtelemetry.ActorKindAnonymous), stored.ActorKind)
	require.Equal(t, requestID, stored.RequestID)
	require.Equal(t, "192.0.2.1", stored.SourceIP)
	require.Equal(t, string(sharedtelemetry.AuthorizationDeniedAuthenticationRequired), stored.Reason)
	require.Equal(t, "/test.v1.PrivateService/GetSecret", stored.AttemptedAction)
	require.Equal(t, sharedtelemetry.AuthorizationProcedureInvokePermission, stored.Permission)

	var count int64
	require.NoError(t, stack.DB.Table("public.security_access").Where("request_id = ? AND action = ?", requestID, sharedtelemetry.SecurityAuthorizationDenied).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

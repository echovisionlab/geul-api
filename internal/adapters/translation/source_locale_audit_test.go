package translationadapter

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recordingSourceLocaleAuditAppender struct {
	records []sharedtelemetry.AuditRecord
}

func TestLegalSourceLocaleAuditPortsKeepPolicyIdentity(t *testing.T) {
	memberID := uuid.NewString()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.62")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, table := range []string{"privacy_history", "terms_history"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id TEXT PRIMARY KEY, version INTEGER NOT NULL)").Error)
	}

	for _, testCase := range []struct {
		entityType string
		table      string
		policyType sharedtelemetry.AuditPolicyType
	}{
		{entityType: "privacy", table: "privacy_history", policyType: sharedtelemetry.AuditPolicyTypePrivacy},
		{entityType: "terms", table: "terms_history", policyType: sharedtelemetry.AuditPolicyTypeTerms},
	} {
		t.Run(testCase.entityType, func(t *testing.T) {
			writer := &recordingSourceLocaleAuditAppender{}
			ports, buildErr := buildDomainPorts(defaultDomainRegistrations(nil, writer))
			require.NoError(t, buildErr)
			registry := &DomainRegistry{ports: ports}
			entityID := uuid.NewString()
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.table+" (id, version) VALUES (?, 7)", entityID,
			).Error)

			require.NoError(t, registry.AppendSourceLocaleAudit(
				ctx, db, testCase.entityType, entityID, "en", "ko",
			))
			require.Len(t, writer.records, 1)
			record := writer.records[0]
			require.Equal(t, sharedtelemetry.AuditLegalPolicyUpdated, record.Action)
			require.Equal(t, "legal_policy", record.TargetType)
			require.Equal(t, entityID, record.TargetID)
			require.Equal(t, testCase.policyType, record.PolicyType)
			require.NotNil(t, record.VersionNumber)
			require.EqualValues(t, 7, *record.VersionNumber)
			require.Equal(t, []string{"source_locale"}, record.ChangedFields)
			require.Equal(t, "en", record.PreviousLocale)
			require.Equal(t, "ko", record.NewLocale)
		})
	}
}

func (writer *recordingSourceLocaleAuditAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	writer.records = append(writer.records, record)
	return nil
}

func TestSourceLocaleAuditPortsUseReviewedTypedBuilders(t *testing.T) {
	memberID := uuid.NewString()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.61")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})

	for _, testCase := range []struct {
		name       string
		entityType string
		action     sharedtelemetry.AuditAction
		targetType string
	}{
		{name: "post", entityType: "post", action: sharedtelemetry.AuditPostUpdated, targetType: "post"},
		{name: "page", entityType: "page", action: sharedtelemetry.AuditPageUpdated, targetType: "page"},
		{name: "work", entityType: "work", action: sharedtelemetry.AuditWorkUpdated, targetType: "work"},
		{name: "post series", entityType: "series", action: sharedtelemetry.AuditPostSeriesUpdated, targetType: "post_series"},
		{name: "program event", entityType: "program_event", action: sharedtelemetry.AuditProgramEventUpdated, targetType: "program_event"},
		{name: "menu", entityType: "menu", action: sharedtelemetry.AuditMenuUpdated, targetType: "menu"},
		{name: "campaign", entityType: "campaign", action: sharedtelemetry.AuditCampaignUpdated, targetType: "campaign"},
		{name: "form", entityType: "form", action: sharedtelemetry.AuditFormUpdated, targetType: "form"},
		{name: "email template", entityType: "email_template", action: sharedtelemetry.AuditEmailTemplateUpdated, targetType: "email_template"},
		{name: "email layout", entityType: "email_layout", action: sharedtelemetry.AuditEmailLayoutUpdated, targetType: "email_layout"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			writer := &recordingSourceLocaleAuditAppender{}
			ports, buildErr := buildDomainPorts(defaultDomainRegistrations(nil, writer))
			require.NoError(t, buildErr)
			registry := &DomainRegistry{ports: ports}
			entityID := uuid.NewString()

			require.NoError(t, registry.AppendSourceLocaleAudit(
				ctx, nil, testCase.entityType, entityID, "en", "ko",
			))
			require.Len(t, writer.records, 1)
			record := writer.records[0]
			require.Equal(t, testCase.action, record.Action)
			require.Equal(t, testCase.targetType, record.TargetType)
			require.Equal(t, entityID, record.TargetID)
			require.Equal(t, memberID, record.MemberID)
			require.Equal(t, requestContext.RequestID, record.RequestID)
			require.Equal(t, []string{"source_locale"}, record.ChangedFields)
			require.Equal(t, "en", record.PreviousLocale)
			require.Equal(t, "ko", record.NewLocale)
		})
	}
}

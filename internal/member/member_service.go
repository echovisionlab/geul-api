package member

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"gorm.io/gorm"
)

const memberNicknameMaxLength = 100

type MemberService struct {
	managev1connect.UnimplementedMemberServiceHandler
	db                     *gorm.DB
	cdnDomain              string
	spicedb                *auth.SpiceDBClient
	identity               auth.IdentityManager
	fileService            FileDeleter
	personalDataAccess     *securityaccess.Recorder
	auditWriter            domainaudit.Appender
	accountSummaryReader   AccountSummaryReader
	accountEmailProjection AccountEmailProjection

	siteOrigin            string
	welcomeEmailPublisher EmailCommandPublisher
	onboardingMetrics     memberOnboardingMetrics
}

type MemberServiceOption func(*MemberService)

func WithAccountSummaryReader(reader AccountSummaryReader) MemberServiceOption {
	return func(service *MemberService) { service.accountSummaryReader = reader }
}

func WithAccountEmailProjection(projection AccountEmailProjection) MemberServiceOption {
	return func(service *MemberService) { service.accountEmailProjection = projection }
}

type memberAuditAppender interface {
	securityaccess.Appender
	domainaudit.Appender
}

func NewAuditedMemberService(
	db *gorm.DB,
	cdnDomain string,
	spicedb *auth.SpiceDBClient,
	identity auth.IdentityManager,
	fileService FileDeleter,
	siteOrigin string,
	welcomeEmailPublisher EmailCommandPublisher,
	auditWriter memberAuditAppender,
	options ...MemberServiceOption,
) *MemberService {
	service := NewMemberService(db, cdnDomain, spicedb, identity, fileService, siteOrigin, welcomeEmailPublisher, options...)
	service.personalDataAccess = securityaccess.New(auditWriter)
	service.auditWriter = auditWriter
	return service
}

type memberOnboardingMetrics struct {
	welcomePublishCounter otelmetric.Int64Counter
}

func newMemberOnboardingMetrics() memberOnboardingMetrics {
	counter, err := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("member")).Int64Counter(
		"member_onboarding_welcome_publish_total",
		otelmetric.WithDescription("Counts welcome command outcomes after a committed Member onboarding transition."),
	)
	if err != nil {
		slog.Warn("Failed to create Member onboarding welcome counter", "error", err)
		return memberOnboardingMetrics{}
	}
	return memberOnboardingMetrics{welcomePublishCounter: counter}
}

func (m memberOnboardingMetrics) recordWelcomePublish(ctx context.Context, outcome string) {
	if m.welcomePublishCounter == nil {
		return
	}
	m.welcomePublishCounter.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("outcome", outcome)))
}

const memberAdminPendingDeletionSQL = `EXISTS (
	SELECT 1
	FROM user_deletion_request AS deletion
	WHERE deletion.member_id = m.id
	  AND deletion.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
	  AND deletion.scheduled_at IS NOT NULL
)`

const memberAdminBannedSQL = `(
	COALESCE((i.metadata_admin ->> 'banned')::boolean, false)
	OR COALESCE(i.state::text = 'inactive', false)
)`

const memberAdminStatusSQL = `(CASE
	WHEN m.deleted_at IS NOT NULL OR i.id IS NULL THEN 'deleted'
	WHEN ` + memberAdminPendingDeletionSQL + ` THEN 'pending_deletion'
	WHEN ` + memberAdminBannedSQL + ` THEN 'banned'
	ELSE 'active'
END)`

var memberAdminFilterConfig = queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type:          queryutil.TypeText,
		AllowedOps:    queryutil.SearchOps,
		SearchColumns: []string{"m.nickname", "COALESCE(m.primary_email, '')"},
	},
	"id": {Column: "m.id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps},
	"nickname": {
		Column: "m.nickname", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"email": {
		Column: "m.primary_email", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"banned": {
		Column: memberAdminBannedSQL, Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps,
	},
	"status": {
		Column:     memberAdminStatusSQL,
		Type:       queryutil.TypeEnum,
		AllowedOps: queryutil.EnumOps,
		EnumValues: []string{"active", "banned", "pending_deletion", "deleted"},
	},
	"newsletter_subscribed": {
		Column: "(ns.identity_id IS NOT NULL)", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps,
	},
	"created_at": {
		Column: "m.created_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
	},
}}

var memberAdminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"nickname":              "m.nickname",
		"email":                 "m.primary_email",
		"newsletter_subscribed": "(ns.identity_id IS NOT NULL)",
		"created_at":            "m.created_at",
	},
	DefaultSort: "m.created_at DESC, m.id DESC",
}

var memberTagAdminFilterConfig = queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{"t.name"},
	},
	"name": {
		Column: "t.name", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"created_at": {
		Column: "t.created_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
	},
}}

var memberTagAdminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "t.name",
		"user_count": "COUNT(m.member_id)",
		"created_at": "t.created_at",
	},
	DefaultSort: "t.name ASC, t.id ASC",
}

func NewMemberService(
	db *gorm.DB,
	cdnDomain string,
	spicedb *auth.SpiceDBClient,
	identity auth.IdentityManager,
	fileService FileDeleter,
	siteOrigin string,
	welcomeEmailPublisher EmailCommandPublisher,
	options ...MemberServiceOption,
) *MemberService {
	if db == nil || spicedb == nil || identity == nil || fileService == nil || welcomeEmailPublisher == nil {
		panic("member service dependencies are required")
	}
	service := &MemberService{
		db:                    db,
		cdnDomain:             cdnDomain,
		spicedb:               spicedb,
		identity:              identity,
		fileService:           fileService,
		siteOrigin:            strings.TrimRight(strings.TrimSpace(siteOrigin), "/"),
		welcomeEmailPublisher: welcomeEmailPublisher,
		onboardingMetrics:     newMemberOnboardingMetrics(),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *MemberService) requireAdminMember(ctx context.Context) (*auth.UserInfo, error) {
	return authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
}

func (s *MemberService) isGlobalAuthor(ctx context.Context) (bool, error) {
	_, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return false, err
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return false, errs.Internal(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, errs.InvalidSession()
	}
	allowed, err := s.spicedb.Can(ctx, decision)
	if err != nil {
		return false, errs.DependencyUnavailable("SpiceDB")
	}
	return allowed, nil
}

package campaignpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	emailcontentpostgres "github.com/echovisionlab/geul-api/internal/emailcontent/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type RecipientAuthorizer interface {
	LookupGlobalSubjects(context.Context, policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error)
}

func NewUnits(db *gorm.DB, ports Ports) (campaigndomain.Units, error) {
	if db == nil {
		return nil, errors.New("campaign store database is required")
	}
	var missing []string
	for name, absent := range map[string]bool{"ContentBlocks": ports.ContentBlocks == nil, "Publisher": ports.Publisher == nil, "Routes": ports.Routes == nil, "EmailAuthoring": ports.EmailAuthoring == nil, "EmailRendering": ports.EmailRendering == nil, "AudienceTargets": ports.AudienceTargets == nil, "Members": ports.Members == nil, "Checkpoints": ports.Checkpoints == nil, "SpiceDB": ports.SpiceDB == nil} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, errors.New("campaign store ports are required: " + strings.Join(missing, ", "))
	}
	return mutation.Postgres(db, func(tx *gorm.DB) campaigndomain.Store { return NewStore(tx, ports) }), nil
}
func NewStore(tx *gorm.DB, ports Ports) campaigndomain.Store {
	return &store{Store: emailcontentpostgres.New(tx, ports.ContentBlocks, "campaign"), tx: tx, ports: ports}
}
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) campaigndomain.Joined {
	joined := campaigndomain.Joined{Store: NewStore(tx, ports)}
	if audit != nil {
		joined.Audit = boundAudit{audit: audit, tx: tx}
	}
	return joined
}

type boundAudit struct {
	audit domainaudit.Appender
	tx    *gorm.DB
}

func (b boundAudit) AppendDomainAuditInTransaction(ctx context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return b.audit.AppendDomainAuditInTransaction(ctx, b.tx, record)
}

type store struct {
	emailcontentpostgres.Store
	tx    *gorm.DB
	ports Ports
}

var _ campaigndomain.Store = (*store)(nil)

type sentinelError struct {
	error
	sentinel error
}

func (e sentinelError) Is(target error) bool { return target == e.sentinel }
func (e sentinelError) Unwrap() error        { return e.error }
func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinelError{error: err, sentinel: sentinel}
	}
	return err
}
func (s *store) EnsureCampaignRoute(ctx context.Context, id string) error {
	return s.ports.Routes.EnsureResourceRouteAvailableInTx(ctx, s.tx, "campaign", "campaigns", id)
}
func (s *store) LockLayouts(ctx context.Context, ids ...string) (map[string]campaigndomain.CampaignLayoutReference, error) {
	return s.ports.EmailAuthoring.LockLayoutsForCampaign(ctx, s.tx, ids...)
}
func (s *store) LayoutSnapshot(ctx context.Context, id string) ([]campaigndomain.CampaignLayoutLocaleSnapshot, error) {
	return s.ports.EmailAuthoring.LoadLayoutSnapshot(ctx, s.tx, id)
}
func (s *store) LockAudienceTarget(ctx context.Context, id string) (campaigndomain.CampaignAudienceTarget, error) {
	target, err := s.ports.AudienceTargets.LockTarget(ctx, s.tx, id)
	return target, notFound(err, campaigndomain.ErrAudienceNotFound)
}
func (s *store) ResolveSessionPrincipal(ctx context.Context, id string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, id)
}
func (s *store) LockMembers(ctx context.Context, ids []string) ([]string, error) {
	return s.ports.Members.LockMembers(ctx, s.tx, ids)
}
func (s *store) RequireCurrentContributors(ctx context.Context, id string, members []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN, id, members)
}
func (s *store) EmailRenderData(ctx context.Context, cdn, origin, locale string, input map[string]string) map[string]string {
	return s.ports.EmailRendering.BuildRenderData(ctx, s.tx, cdn, origin, locale, input)
}
func (s *store) WrapWithLayout(ctx context.Context, id, locale, html string, data map[string]string) (string, error) {
	return s.ports.EmailRendering.WrapWithLayout(ctx, s.tx, id, locale, html, data)
}
func (s *store) CheckTestSendReadiness(ctx context.Context, recipient string) error {
	return email.CheckTestSendReadiness(ctx, emailpostgres.Bind(s.tx), recipient)
}
func (s *store) Enqueue(ctx context.Context, queue, id string, message proto.Message) error {
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	return s.ports.Publisher.EnqueueProtobufWithExecutor(ctx, executor, queue, id, message)
}
func (s *store) PrepareLegalNoticePreview(ctx context.Context, run campaigndomain.DeliveryRun, now time.Time) error {
	if s.ports.LegalNotice == nil {
		return domainerr.DependencyUnavailable("Legal notice delivery")
	}
	return s.ports.LegalNotice.PrepareAutomaticPreviewShareLink(ctx, s.tx, run, now)
}

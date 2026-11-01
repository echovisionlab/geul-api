// Package emailauthoringpostgres is the PostgreSQL emailauthoring.Store: the
// Email Template and Email Layout tables, the template's email content, and
// the delivery history, rendering, Member and collaboration runtimes an
// authoring transaction drives, bound to one gorm transaction.
package emailauthoringpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailcontentpostgres "github.com/echovisionlab/geul-api/internal/emailcontent/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// DeliveryReferences is Campaign's delivery history that freezes or retains
// an authoring resource, read in the authoring transaction; Campaign owns it
// (emailauthoringadapter.CampaignDeliveryReferences).
type DeliveryReferences interface {
	TemplateDeliveryRunCounts(context.Context, *gorm.DB, []string) (map[string]int32, error)
	LayoutExternalReferenceCounts(context.Context, *gorm.DB, []string) (map[string]emailauthoring.LayoutExternalReferenceCounts, error)
	RequireTemplateMutable(context.Context, *gorm.DB, string) error
	RequireLayoutMutable(context.Context, *gorm.DB, string) error
	DetachTemplateHistory(context.Context, *gorm.DB, string) error
	DetachLayoutHistory(context.Context, *gorm.DB, string) error
}

// Members locks the Members an authoring write credits; Member owns them
// (memberadapter.ContributorMembers).
type Members interface {
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error)
}

// Rendering is the delivery runtime of previews and test sends
// (emailauthoringadapter.Runtime).
type Rendering interface {
	ResolveLocalizedTemplate(context.Context, *gorm.DB, model.EmailTemplate, string) (model.EmailTemplate, error)
	BuildEmailRenderData(context.Context, *gorm.DB, string, string, string, map[string]string) map[string]string
}

// Ports are the other domains and shared runtimes the Store drives in the
// authoring transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	References    DeliveryReferences
	Members       Members
	Checkpoints   persistencecheckpoint.ContributorFence
	Rendering     Rendering
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "References": p.References == nil, "Members": p.Members == nil,
		"Checkpoints": p.Checkpoints == nil, "Rendering": p.Rendering == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("email authoring store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Email Authoring use
// cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[emailauthoring.Store], error) {
	if db == nil {
		return nil, errors.New("email authoring store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) emailauthoring.Store { return NewStore(tx, ports) }), nil
}

// NewStore binds the Store to tx. Only the methods whose ports ports names
// are usable, so another domain's pipeline binds what it needs.
func NewStore(tx *gorm.DB, ports Ports) emailauthoring.Store {
	return &store{
		Store: emailcontentpostgres.New(tx, ports.ContentBlocks, "email_template"),
		tx:    tx, ports: ports,
	}
}

// Bind joins the Store and audit to tx, a transaction another domain's
// pipeline opened (translation delivery and interchange).
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) emailauthoring.Joined {
	return emailauthoring.Joined{Store: NewStore(tx, ports), Audit: domainaudit.Bind(tx, audit)}
}

type store struct {
	emailcontentpostgres.Store
	tx    *gorm.DB
	ports Ports
}

var _ emailauthoring.Store = (*store)(nil)

// sentinelError keeps the driver's error readable and in the chain while it
// answers a domain sentinel.
type sentinelError struct {
	error
	sentinel error
}

func (err sentinelError) Is(target error) bool { return target == err.sentinel }
func (err sentinelError) Unwrap() error        { return err.error }

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinelError{error: err, sentinel: sentinel}
	}
	return err
}

func keyTaken(err, sentinel error) error {
	if dberrors.IsUniqueViolation(err) {
		return sentinelError{error: err, sentinel: sentinel}
	}
	return err
}

func (s *store) TemplateDeliveryRunCounts(ctx context.Context, templateIDs []string) (map[string]int32, error) {
	return s.ports.References.TemplateDeliveryRunCounts(ctx, s.tx, templateIDs)
}

func (s *store) LayoutExternalReferenceCounts(ctx context.Context, layoutIDs []string) (map[string]emailauthoring.LayoutExternalReferenceCounts, error) {
	return s.ports.References.LayoutExternalReferenceCounts(ctx, s.tx, layoutIDs)
}

func (s *store) RequireTemplateMutable(ctx context.Context, templateID string) error {
	return s.ports.References.RequireTemplateMutable(ctx, s.tx, templateID)
}

func (s *store) RequireLayoutMutable(ctx context.Context, layoutID string) error {
	return s.ports.References.RequireLayoutMutable(ctx, s.tx, layoutID)
}

func (s *store) DetachTemplateHistory(ctx context.Context, templateID string) error {
	return s.ports.References.DetachTemplateHistory(ctx, s.tx, templateID)
}

func (s *store) DetachLayoutHistory(ctx context.Context, layoutID string) error {
	return s.ports.References.DetachLayoutHistory(ctx, s.tx, layoutID)
}

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) LockMembers(ctx context.Context, memberIDs []string) ([]string, error) {
	return s.ports.Members.LockMembers(ctx, s.tx, memberIDs)
}

func (s *store) RequireCurrentTemplateContributors(ctx context.Context, templateID string, contributors []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(
		ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE, templateID, contributors,
	)
}

func (s *store) RequireCurrentLayoutContributors(ctx context.Context, layoutID string, contributors []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(
		ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_LAYOUT, layoutID, contributors,
	)
}

func (s *store) LocalizedTemplate(ctx context.Context, template emailauthoring.Template, locale string) (emailauthoring.Template, error) {
	resolved, err := s.ports.Rendering.ResolveLocalizedTemplate(ctx, s.tx, templateRow(template), locale)
	if err != nil {
		return emailauthoring.Template{}, err
	}
	localized := templateOf(resolved)
	localized.DeliveryRunCount = template.DeliveryRunCount
	return localized, nil
}

func (s *store) EmailRenderData(ctx context.Context, cdnDomain, siteOrigin, locale string, input map[string]string) map[string]string {
	return s.ports.Rendering.BuildEmailRenderData(ctx, s.tx, cdnDomain, siteOrigin, locale, input)
}

func (s *store) CheckTestSendReadiness(ctx context.Context, recipient string) error {
	return email.CheckTestSendReadiness(ctx, emailpostgres.NewStore(s.tx), recipient)
}

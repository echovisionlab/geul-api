// Package public serves the published Terms and Privacy versions to the
// public site.
package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Deps are the collaborators of the public legal use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB            *gorm.DB
	ContentBlocks *contentblock.Store
	Media         legaldomain.PublicMedia
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "ContentBlocks": d.ContentBlocks == nil, "Media": d.Media == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public legal dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Terms and Privacy use cases. Each takes the
// legaldomain.PolicyKind it reads.
type Service struct {
	db            *gorm.DB
	contentBlocks *contentblock.Store
	media         legaldomain.PublicMedia
}

// NewService validates deps and returns the public legal use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, contentBlocks: deps.ContentBlocks, media: deps.Media}, nil
}

// publicKind is what the public routes read of a policy kind.
type publicKind struct {
	entity    string
	table     string
	scheduled model.LegalPolicyStatus
	active    model.LegalPolicyStatus
	archived  model.LegalPolicyStatus
	// shareLinkEntityType is the stored entity type of the kind's Share
	// Links.
	shareLinkEntityType string
	localization        publiccontent.Spec
	// serviceName names the kind's public service in failure records.
	serviceName string
}

func publicKindOf(kind legaldomain.PolicyKind) (publicKind, error) {
	scheduled, known := legaldomain.PolicyStatus(kind, "scheduled")
	active, _ := legaldomain.PolicyStatus(kind, "active")
	archived, _ := legaldomain.PolicyStatus(kind, "archived")
	shareLinkEntityType, _ := kind.ShareLinkEntityType()
	if !known {
		return publicKind{}, fmt.Errorf("unsupported legal policy kind %q", kind)
	}
	entity := string(kind)
	return publicKind{
		entity: entity, table: entity + "_history",
		scheduled: scheduled, active: active, archived: archived,
		shareLinkEntityType: shareLinkEntityType,
		localization: publiccontent.Spec{
			EntityType: entity, TableName: entity + "_translation", SelectClause: "locale, title",
		},
		serviceName: "public." + strings.ToUpper(entity[:1]) + entity[1:],
	}, nil
}

// publicPolicyRow is a published row of terms_history or privacy_history.
type publicPolicyRow struct {
	ID             string                  `gorm:"column:id;primaryKey"`
	Version        int                     `gorm:"column:version"`
	Title          string                  `gorm:"column:title"`
	Status         model.LegalPolicyStatus `gorm:"column:status"`
	EffectiveFrom  *time.Time              `gorm:"column:effective_from"`
	EffectiveUntil *time.Time              `gorm:"column:effective_until"`
}

// PublicPolicy is a version as the public site shows it. A scheduled
// version announced beside the current one carries only its identity, title
// and effective dates.
type PublicPolicy struct {
	ID             string
	Version        int32
	Title          string
	Document       *contentv1.LocalizedRichTextDocument
	Revision       string
	EffectiveFrom  *time.Time
	EffectiveUntil *time.Time
	Localization   *publiccontent.Selection
	OGAsset        *commonv1.AssetRef
}

// PublicPolicies is the version a public read shows and the scheduled one
// it announces; either may be absent.
type PublicPolicies struct {
	Current   *PublicPolicy
	Scheduled *PublicPolicy
}

// GetPolicyQuery reads the current version, a published version by ID, or
// a version a Share Link opens.
type GetPolicyQuery struct {
	ID             string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
	Now            time.Time
}

// GetPolicy returns the version the query names: with ShareToken the
// version its Share Link opens, with ID alone that active or archived
// version, and otherwise the effective active version and the next
// scheduled one.
func (s *Service) GetPolicy(ctx context.Context, kind legaldomain.PolicyKind, query GetPolicyQuery) (PublicPolicies, error) {
	descriptor, err := publicKindOf(kind)
	if err != nil {
		return PublicPolicies{}, err
	}
	switch {
	case query.ShareToken != "":
		return s.loadShared(ctx, descriptor, query)
	case query.ID != "":
		return s.loadHistory(ctx, descriptor, query)
	default:
		return s.loadCurrent(ctx, descriptor, query)
	}
}

// ListArchivedPoliciesQuery pages through archived versions. Limit
// defaults to 10 and is at most 100.
type ListArchivedPoliciesQuery struct {
	Limit  int32
	Offset int32
}

// PolicySummary is an archived version in the public history list.
type PolicySummary struct {
	ID             string
	Version        int32
	Title          string
	EffectiveFrom  *time.Time
	EffectiveUntil *time.Time
}

// ArchivedPolicies is one page of archived versions and their total.
type ArchivedPolicies struct {
	Items []PolicySummary
	Total int32
}

// ListArchivedPolicies returns archived versions, latest effective first.
func (s *Service) ListArchivedPolicies(ctx context.Context, kind legaldomain.PolicyKind, query ListArchivedPoliciesQuery) (_ ArchivedPolicies, returnErr error) {
	descriptor, err := publicKindOf(kind)
	if err != nil {
		return ArchivedPolicies{}, err
	}
	defer failurecause.Annotate(&returnErr, slog.String("service", descriptor.serviceName))

	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 100)
	var total int64
	s.db.WithContext(ctx).Table(descriptor.table).Where("status = ?", descriptor.archived).Count(&total)
	var rows []publicPolicyRow
	if err := s.db.WithContext(ctx).Table(descriptor.table).
		Where("status = ?", descriptor.archived).
		Order("effective_from DESC").
		Limit(int(limit)).Offset(int(max(query.Offset, 0))).
		Find(&rows).Error; err != nil {
		return ArchivedPolicies{}, err
	}
	items := make([]PolicySummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, PolicySummary{
			ID: row.ID, Version: int32(row.Version), Title: resolveCanonicalLegalPublicTitle(row.Title, nil),
			EffectiveFrom: row.EffectiveFrom, EffectiveUntil: row.EffectiveUntil,
		})
	}
	return ArchivedPolicies{Items: items, Total: int32(total)}, nil
}

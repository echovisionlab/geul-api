package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func normalizeCampaignNameAndSubject(rawName, rawSubject string) (string, string, error) {
	name := strings.TrimSpace(rawName)
	subject := strings.TrimSpace(rawSubject)

	switch {
	case name == "" && subject == "":
		return "", "", domainerr.Required("name")
	case name == "":
		name = subject
	case subject == "":
		subject = name
	}

	return name, subject, nil
}

// Deps are the collaborators of the Campaign management use cases. Every
// field except CDNDomain and SiteOrigin is required; NewService rejects a
// missing one.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	// Runtime queues test sends, reserves a Campaign's route, normalizes
	// locales and records recipient list access.
	Runtime         Runtime
	EmailAuthoring  CampaignEmailAuthoringPort
	EmailRendering  CampaignEmailRenderingPort
	EmailDelivery   CampaignDeliveryPort
	AudienceTargets CampaignAudienceTargetPort
	Audit           domainaudit.Appender
	// CDNDomain is the asset host of preview render data; empty keeps
	// asset links relative.
	CDNDomain string
	// SiteOrigin is the site origin of previews and test sends; empty
	// keeps their links on the example host.
	SiteOrigin string
}

func (d Deps) validate() error {
	return requireDependencies("campaign", map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil,
		"Runtime": d.Runtime == nil, "EmailAuthoring": d.EmailAuthoring == nil, "EmailRendering": d.EmailRendering == nil,
		"EmailDelivery": d.EmailDelivery == nil, "AudienceTargets": d.AudienceTargets == nil, "Audit": d.Audit == nil,
	})
}

// Service holds the Campaign management use cases. The Connect adapter
// translates its requests into their commands and queries.
type Service struct {
	db              *gorm.DB
	spiceDB         *auth.SpiceDBClient
	contentBlocks   *contentblock.Store
	runtime         Runtime
	emailAuthoring  CampaignEmailAuthoringPort
	emailRendering  CampaignEmailRenderingPort
	emailDelivery   CampaignDeliveryPort
	audienceTargets CampaignAudienceTargetPort
	mutations       *mutation.Runner
	cdnDomain       string
	siteOrigin      string
}

// NewService validates deps and returns the Campaign management use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, contentBlocks: deps.ContentBlocks, runtime: deps.Runtime,
		emailAuthoring: deps.EmailAuthoring, emailRendering: deps.EmailRendering, emailDelivery: deps.EmailDelivery,
		audienceTargets: deps.AudienceTargets, mutations: mutations,
		cdnDomain: deps.CDNDomain, siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
	}, nil
}

// requireAdminCan authorizes an administrator read; an ID that cannot name
// the Campaign is rejected first.
func (s *Service) requireAdminCan(ctx context.Context, can policyv1.Can, canErr error) error {
	if canErr != nil {
		return domainerr.InvalidField("id", canErr.Error())
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// adminAccess is the in-transaction authority of an administrator mutation
// of one Campaign; an ID that cannot name it is rejected before any lock.
func adminAccess(can policyv1.Can, err error) (mutation.Access, error) {
	if err != nil {
		return mutation.Access{}, domainerr.InvalidField("id", err.Error())
	}
	return mutation.FreshAdminCan(can, nil), nil
}

func errCampaignNotFound() error {
	return domainerr.New(domainerr.NotFound, "", "campaign not found")
}

// lockCampaign locks the Campaign row for update.
func lockCampaign(ctx context.Context, tx *gorm.DB, campaignID string, campaign *model.Campaign) error {
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(campaign, "id = ?", campaignID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errCampaignNotFound()
		}
		return err
	}
	return nil
}

// GetCampaignQuery reads one Campaign.
type GetCampaignQuery struct {
	ID string
}

// GetCampaign returns the Campaign with its source document.
func (s *Service) GetCampaign(ctx context.Context, query GetCampaignQuery) (*managev1.Campaign, error) {
	can, canErr := policyv1.Campaign.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	if query.ID == "" {
		return nil, domainerr.Required("id")
	}
	campaign, err := s.loadCampaign(ctx, query.ID)
	if err != nil {
		return nil, err
	}
	return s.toProtoCampaign(ctx, &campaign)
}

func (s *Service) loadCampaign(ctx context.Context, campaignID string) (model.Campaign, error) {
	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", campaignID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return campaign, errCampaignNotFound()
		}
		return campaign, err
	}
	return campaign, nil
}

// campaignSortConfig defines allowed sort fields for campaigns
var campaignSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"subject":    "subject",
		"status":     "status",
		"sent_count": "sent_count",
		"sentCount":  "sent_count", // camelCase alias
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "created_at DESC, id ASC",
}

// ListCampaignsQuery pages through Campaigns for administrators.
type ListCampaignsQuery struct {
	// Limit defaults to 20 and is not capped.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// CampaignPage is one page of Campaigns.
type CampaignPage struct {
	Campaigns  []*managev1.Campaign
	Pagination *commonv1.PaginationResponse
}

// ListCampaigns returns one page of Campaigns without their documents.
func (s *Service) ListCampaigns(ctx context.Context, query ListCampaignsQuery) (CampaignPage, error) {
	can, canErr := policyv1.Campaign.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return CampaignPage{}, err
	}
	list, err := campaignFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.Campaign{}), query.Filters)
	if err != nil {
		return CampaignPage{}, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return CampaignPage{}, err
	}
	limit, offset := int32(20), query.Offset
	if query.Limit > 0 {
		limit = query.Limit
	}
	list, err = campaignSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return CampaignPage{}, err
	}
	var campaigns []model.Campaign
	if err := list.Limit(int(limit)).Offset(int(offset)).Find(&campaigns).Error; err != nil {
		return CampaignPage{}, err
	}
	sourceLocales, err := loadCampaignSourceLocales(ctx, s.db, campaigns)
	if err != nil {
		return CampaignPage{}, err
	}
	protoCampaigns := make([]*managev1.Campaign, len(campaigns))
	for i := range campaigns {
		if protoCampaigns[i], err = toProtoCampaignBase(&campaigns[i]); err != nil {
			return CampaignPage{}, err
		}
		protoCampaigns[i].SourceLocale = sourceLocales[campaigns[i].ID]
	}
	return CampaignPage{
		Campaigns: protoCampaigns,
		Pagination: &commonv1.PaginationResponse{
			Total: int32(total), Limit: limit, Offset: offset, HasMore: offset+limit < int32(total),
		},
	}, nil
}

// CreateCampaignTarget is a new Campaign's recipients: every eligible
// Member, or one audience segment.
type CreateCampaignTarget struct {
	All       bool
	SegmentID *string
	// allMissing marks a request that chose the all target without its
	// value, which validation rejects in its own words.
	allMissing bool
}

// CreateCampaignCommand creates a draft Campaign.
type CreateCampaignCommand struct {
	Name         string
	Subject      string
	SourceLocale string
	Target       CreateCampaignTarget
}

// CreateCampaign creates a draft Campaign with its source subject and an
// empty source document, reserves its route and grants its policy.
func (s *Service) CreateCampaign(ctx context.Context, command CreateCampaignCommand) (*managev1.Campaign, error) {
	campaign, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.Campaign]{
		Access: mutation.FreshAdminCan(policyv1.Campaign.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (model.Campaign, error) {
			campaign, err := s.newCampaign(command)
			if err != nil {
				return model.Campaign{}, err
			}
			if campaign.SegmentID != nil {
				if err := s.requireAssignableSegment(ctx, tx, *campaign.SegmentID); err != nil {
					return model.Campaign{}, err
				}
			}
			return campaign, s.persistNewCampaign(ctx, tx, &campaign)
		},
		Audit: func(campaign model.Campaign) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditCampaignCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewCampaignCreatedAuditRecord(metadata, campaign.ID)
			})
		},
		Relationships: func(campaign model.Campaign) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return campaignPolicyLifecycle(campaign.ID)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.toProtoCampaign(ctx, &campaign)
}

// newCampaign validates the command and returns the draft it creates.
func (s *Service) newCampaign(command CreateCampaignCommand) (model.Campaign, error) {
	name, subject, err := normalizeCampaignNameAndSubject(command.Name, command.Subject)
	if err != nil {
		return model.Campaign{}, err
	}
	sourceLocale, err := normalizeCampaignCreateSourceLocale(s.runtime, command.SourceLocale)
	if err != nil {
		return model.Campaign{}, err
	}
	targetMode, segmentID, err := createCampaignTargetDefinition(command.Target)
	if err != nil {
		return model.Campaign{}, err
	}
	return model.Campaign{
		Name: name, Subject: subject, SourceLocale: sourceLocale, Status: string(model.CampaignStatusDraft),
		TargetMode: targetMode, SegmentID: segmentID,
	}, nil
}

// persistNewCampaign stores the draft with its content document, route and
// source subject.
func (s *Service) persistNewCampaign(ctx context.Context, tx *gorm.DB, campaign *model.Campaign) error {
	now := dbtime.Now()
	campaign.CreatedAt, campaign.UpdatedAt = now, now
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile: emailContentProfile, SourceLocale: campaign.SourceLocale,
	})
	if err != nil {
		return campaignContent.NormalizeContentBlockError(err)
	}
	documentID := document.Document.ID.String()
	campaign.ContentDocumentID = &documentID
	if err := tx.WithContext(ctx).Create(campaign).Error; err != nil {
		return err
	}
	if err := s.runtime.EnsureResourceRouteAvailableInTx(ctx, tx, "campaign", "campaigns", campaign.ID); err != nil {
		return err
	}
	_, err = updateCampaignEmailLocaleSubject(ctx, tx, campaign.ID, campaign.SourceLocale, campaign.Subject, now)
	return err
}

// requireAssignableSegment locks the audience segment a Campaign is
// assigned to and rejects a missing or archived one.
func (s *Service) requireAssignableSegment(ctx context.Context, tx *gorm.DB, segmentID string) error {
	target, err := s.audienceTargets.LockTarget(ctx, tx, segmentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("audience_segment", segmentID)
	}
	if err != nil {
		return err
	}
	if target.Archived {
		return domainerr.Precondition("archived audience segment cannot be assigned to a campaign")
	}
	return nil
}

// campaignPolicyLifecycle returns the relationship batch that grants a
// created Campaign's policy and its exact inverse; a deletion swaps the two.
func campaignPolicyLifecycle(campaignID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	touchPolicy, err := policyv1.Campaign.TouchPolicy(campaignID)
	if err != nil {
		return nil, nil, err
	}
	deletePolicy, err := policyv1.Campaign.DeletePolicy(campaignID)
	if err != nil {
		return nil, nil, err
	}
	return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, nil
}

func createCampaignTargetDefinition(target CreateCampaignTarget) (string, *string, error) {
	switch {
	case target.allMissing:
		return "", nil, domainerr.InvalidField("target", "all target is required")
	case target.All:
		return model.CampaignTargetModeAll, nil, nil
	case target.SegmentID != nil:
		segmentID := strings.TrimSpace(*target.SegmentID)
		if segmentID == "" {
			return "", nil, domainerr.InvalidField("target", "segment_id is required")
		}
		if _, err := uuid.Parse(segmentID); err != nil {
			return "", nil, domainerr.InvalidField("target", "segment_id must be a UUID")
		}
		return model.CampaignTargetModeSegment, &segmentID, nil
	default:
		return "", nil, domainerr.InvalidField("target", "target is required")
	}
}

func normalizeCampaignCreateSourceLocale(runtime LocaleNormalizer, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", domainerr.Required("source_locale")
	}
	normalized := runtime.NormalizeSupportedLocale(value)
	if normalized == nil {
		return "", domainerr.InvalidField("source_locale", "unsupported locale")
	}
	return *normalized, nil
}

// UpdateCampaignNameCommand renames a draft Campaign.
type UpdateCampaignNameCommand struct {
	ID   string
	Name string
}

// CampaignNameUpdate is the name a rename left.
type CampaignNameUpdate struct {
	ID        string
	Changed   bool
	Name      string
	UpdatedAt time.Time
}

// UpdateCampaignName renames the draft Campaign; the same name changes
// nothing.
func (s *Service) UpdateCampaignName(ctx context.Context, command UpdateCampaignNameCommand) (CampaignNameUpdate, error) {
	access, err := adminAccess(policyv1.Campaign.Edit(command.ID))
	if err != nil {
		return CampaignNameUpdate{}, err
	}
	var campaign model.Campaign
	return mutation.Run(ctx, s.mutations, mutation.Spec[CampaignNameUpdate]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (CampaignNameUpdate, error) {
			update := CampaignNameUpdate{ID: campaign.ID, Name: campaign.Name, UpdatedAt: campaign.UpdatedAt}
			name := strings.TrimSpace(command.Name)
			if name == "" {
				return update, domainerr.Required("name")
			}
			if !campaignStatusAllowsEdit(campaign.Status) {
				return update, domainerr.Precondition(msgCampaignCannotUpdateSent)
			}
			if campaign.Name == name {
				return update, nil
			}
			now := dbtime.Now()
			if err := tx.WithContext(ctx).Model(&campaign).Updates(structured.Fields{"name": name, "updated_at": now}).Error; err != nil {
				return update, err
			}
			return CampaignNameUpdate{ID: campaign.ID, Changed: true, Name: name, UpdatedAt: now}, nil
		},
		Audit: func(update CampaignNameUpdate) mutation.Audit {
			if !update.Changed {
				return mutation.Unchanged()
			}
			return campaignMetadataAudit(update.ID, []string{"name"})
		},
	})
}

// campaignMetadataAudit is the record of a change to fields of a Campaign's
// configuration.
func campaignMetadataAudit(campaignID string, fields []string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignMetadataAuditRecord(metadata, campaignID, fields)
	})
}

// DeleteCampaignCommand deletes a draft Campaign.
type DeleteCampaignCommand struct {
	ID string
}

// DeleteCampaign deletes a draft Campaign that never sent, with its
// document, and revokes its policy.
func (s *Service) DeleteCampaign(ctx context.Context, command DeleteCampaignCommand) error {
	access, err := adminAccess(policyv1.Campaign.Delete(command.ID))
	if err != nil {
		return err
	}
	var campaign model.Campaign
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			return campaign.ID, s.deleteLockedCampaign(ctx, tx, &campaign)
		},
		Audit: func(campaignID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditCampaignDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewCampaignDeletedAuditRecord(metadata, campaignID)
			})
		},
		Relationships: func(campaignID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			apply, compensate, err := campaignPolicyLifecycle(campaignID)
			return compensate, apply, err
		},
	})
	return err
}

func (s *Service) deleteLockedCampaign(ctx context.Context, tx *gorm.DB, campaign *model.Campaign) error {
	if model.CampaignStatus(campaign.Status) != model.CampaignStatusDraft {
		return domainerr.Precondition("only draft campaigns can be deleted")
	}
	hasHistory, err := s.emailDelivery.HasHistory(ctx, tx, campaign.ID)
	if err != nil {
		return err
	}
	if hasHistory {
		return domainerr.Precondition("campaign delivery history must be preserved")
	}
	documentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, campaign.ID, false)
	if err != nil {
		return err
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, campaignEmailDeleteContentFence(campaign.ID)); err != nil {
		return campaignContent.NormalizeContentBlockError(err)
	}
	if err := tx.WithContext(ctx).Delete(campaign).Error; err != nil {
		if dberrors.IsForeignKeyViolation(err) {
			return domainerr.Precondition("campaign delivery history or another durable reference exists").WithCause(err)
		}
		return err
	}
	return nil
}

func (s *Service) toProtoCampaign(ctx context.Context, c *model.Campaign) (*managev1.Campaign, error) {
	campaign, err := toProtoCampaignBase(c)
	if err != nil {
		return nil, err
	}
	projection, err := loadCampaignEmailEditorProjection(ctx, s.db, s.contentBlocks, c.ID)
	if err != nil {
		return nil, err
	}
	campaign.Document = projection.Document
	campaign.DocumentRevision = projection.Revision
	campaign.SourceLocale = projection.SourceLocale
	return campaign, nil
}

// toProtoCampaignBase keeps list projections bounded. The editor fetches the
// aggregate through GetCampaign or the internal LoadDocument bootstrap.
func toProtoCampaignBase(c *model.Campaign) (*managev1.Campaign, error) {
	recipientScope, err := campaignRecipientScopeToProto(c.RecipientScope)
	if err != nil {
		return nil, err
	}
	campaign := &managev1.Campaign{
		Id:         c.ID,
		Name:       c.Name,
		Subject:    c.Subject,
		Status:     campaignStatusToProto(model.CampaignStatus(c.Status)),
		TargetMode: campaignTargetModeToProto(c.TargetMode),
		SentCount:  int32(c.SentCount),
		CreatedAt:  timestamppb.New(c.CreatedAt),
		UpdatedAt:  timestamppb.New(c.UpdatedAt),
	}
	if c.ScheduledAt != nil {
		campaign.ScheduledAt = timestamppb.New(*c.ScheduledAt)
	}
	if c.SentAt != nil {
		campaign.SentAt = timestamppb.New(*c.SentAt)
	}
	if c.SegmentID != nil {
		campaign.SegmentId = c.SegmentID
	}
	if c.LayoutID != nil {
		campaign.LayoutId = c.LayoutID
	}
	campaign.RecipientScope = recipientScope
	return campaign, nil
}

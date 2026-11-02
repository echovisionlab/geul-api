package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
	Units      Units
	SpiceDB    mutation.Authorizer
	Fence      identitystate.Fence
	Audit      domainaudit.Appender
	Runtime    Runtime
	Clock      func() time.Time
	CDNDomain  string
	SiteOrigin string
}

func (d Deps) validate() error {
	return requireDependencies("campaign", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil, "Runtime": d.Runtime == nil, "Clock": d.Clock == nil,
	})
}

type Service struct {
	units      Units
	spiceDB    mutation.Authorizer
	runtime    Runtime
	mutations  *mutation.StoreRunner[Store]
	now        func() time.Time
	cdnDomain  string
	siteOrigin string
}

// NewService validates deps and returns the Campaign management use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, runtime: deps.Runtime, mutations: mutations, now: deps.Clock, cdnDomain: deps.CDNDomain, siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/")}, nil
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
func lockCampaign(ctx context.Context, store Store, campaignID string, campaign *Campaign) error {
	row, err := store.LockCampaign(ctx, campaignID)
	if errors.Is(err, ErrCampaignNotFound) {
		return errCampaignNotFound()
	}
	*campaign = row
	return err
}

// GetCampaignQuery reads one Campaign.
type GetCampaignQuery struct {
	ID string
}

// GetCampaign returns the Campaign with its source document.
func (s *Service) GetCampaign(ctx context.Context, query GetCampaignQuery) (*CampaignDocument, error) {
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
	return s.campaignDocument(ctx, &campaign)
}

func (s *Service) loadCampaign(ctx context.Context, campaignID string) (Campaign, error) {
	var row Campaign
	err := s.units.Read(ctx, func(store Store) error { var err error; row, err = store.FindCampaign(ctx, campaignID); return err })
	if errors.Is(err, ErrCampaignNotFound) {
		return row, errCampaignNotFound()
	}
	return row, err
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
	Campaigns  []CampaignDocument
	Pagination Pagination
}

// ListCampaigns returns one page of Campaigns without their documents.
func (s *Service) ListCampaigns(ctx context.Context, query ListCampaignsQuery) (CampaignPage, error) {
	can, canErr := policyv1.Campaign.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return CampaignPage{}, err
	}
	limit, offset := int32(20), query.Offset
	if query.Limit > 0 {
		limit = query.Limit
	}
	var rows []Campaign
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, total, err = store.ListCampaigns(ctx, CampaignList{Filters: query.Filters, Sorts: query.Sorts, Limit: int(limit), Offset: int(offset)})
		return err
	}); err != nil {
		return CampaignPage{}, err
	}
	var locales map[string]string

	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		locales, err = loadCampaignSourceLocales(ctx, store, rows)
		return err
	}); err != nil {
		return CampaignPage{}, err
	}
	result := make([]CampaignDocument, len(rows))
	for i, row := range rows {
		if err := validateCampaignRecipientScope(row.RecipientScope); err != nil {
			return CampaignPage{}, err
		}
		row.SourceLocale = locales[row.ID]
		result[i] = CampaignDocument{Campaign: row}
	}
	return CampaignPage{Campaigns: result, Pagination: Pagination{Total: int32(total), Limit: limit, Offset: offset, HasMore: offset+limit < int32(total)}}, nil
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
func (s *Service) CreateCampaign(ctx context.Context, command CreateCampaignCommand) (*CampaignDocument, error) {
	campaign, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Campaign, Store]{
		Access: mutation.FreshAdminCan(policyv1.Campaign.Create()),
		Change: func(ctx context.Context, tx Store) (Campaign, error) {
			campaign, err := s.newCampaign(command)
			if err != nil {
				return Campaign{}, err
			}
			if campaign.SegmentID != nil {
				if err := s.requireAssignableSegment(ctx, tx, *campaign.SegmentID); err != nil {
					return Campaign{}, err
				}
			}
			return campaign, s.persistNewCampaign(ctx, tx, &campaign)
		},
		Audit: func(campaign Campaign) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditCampaignCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewCampaignCreatedAuditRecord(metadata, campaign.ID)
			})
		},
		Relationships: func(campaign Campaign) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return campaignPolicyLifecycle(campaign.ID)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.campaignDocument(ctx, &campaign)
}

// newCampaign validates the command and returns the draft it creates.
func (s *Service) newCampaign(command CreateCampaignCommand) (Campaign, error) {
	name, subject, err := normalizeCampaignNameAndSubject(command.Name, command.Subject)
	if err != nil {
		return Campaign{}, err
	}
	sourceLocale, err := normalizeCampaignCreateSourceLocale(s.runtime, command.SourceLocale)
	if err != nil {
		return Campaign{}, err
	}
	targetMode, segmentID, err := createCampaignTargetDefinition(command.Target)
	if err != nil {
		return Campaign{}, err
	}
	return Campaign{
		Name: name, Subject: subject, SourceLocale: sourceLocale, Status: CampaignStatusDraft,
		TargetMode: targetMode, SegmentID: segmentID,
	}, nil
}

// persistNewCampaign stores the draft with its content document, route and
// source subject.
func (s *Service) persistNewCampaign(ctx context.Context, store Store, campaign *Campaign) error {
	now := s.now().UTC().Truncate(dbtime.Precision)
	campaign.CreatedAt, campaign.UpdatedAt = now, now
	document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: emailContentProfile, SourceLocale: campaign.SourceLocale})
	if err != nil {
		return campaignContent.NormalizeContentBlockError(err)
	}
	documentID := document.Document.ID.String()
	campaign.ContentDocumentID = &documentID
	if err := store.InsertCampaign(ctx, campaign); err != nil {
		return err
	}
	if err := store.EnsureCampaignRoute(ctx, campaign.ID); err != nil {
		return err
	}
	_, err = updateCampaignEmailLocaleSubject(ctx, store, campaign.ID, campaign.SourceLocale, campaign.Subject, now)
	return err
}

// requireAssignableSegment locks the audience segment a Campaign is
// assigned to and rejects a missing or archived one.
func (s *Service) requireAssignableSegment(ctx context.Context, tx Store, segmentID string) error {
	target, err := tx.LockAudienceTarget(ctx, segmentID)
	if errors.Is(err, ErrAudienceNotFound) {
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
		return CampaignTargetModeAll, nil, nil
	case target.SegmentID != nil:
		segmentID := strings.TrimSpace(*target.SegmentID)
		if segmentID == "" {
			return "", nil, domainerr.InvalidField("target", "segment_id is required")
		}
		if _, err := uuid.Parse(segmentID); err != nil {
			return "", nil, domainerr.InvalidField("target", "segment_id must be a UUID")
		}
		return CampaignTargetModeSegment, &segmentID, nil
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
	var campaign Campaign
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[CampaignNameUpdate, Store]{
		Lock: func(ctx context.Context, tx Store) error {
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx Store) (CampaignNameUpdate, error) {
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
			now := s.now().UTC().Truncate(dbtime.Precision)
			if err := tx.UpdateCampaign(ctx, campaign, CampaignChange{Name: &name, UpdatedAt: now}); err != nil {
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
	var campaign Campaign
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, tx Store) error {
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx Store) (string, error) {
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

func (s *Service) deleteLockedCampaign(ctx context.Context, tx Store, campaign *Campaign) error {
	if CampaignStatus(campaign.Status) != CampaignStatusDraft {
		return domainerr.Precondition("only draft campaigns can be deleted")
	}
	hasHistory, err := tx.HasHistory(ctx, campaign.ID)
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
	if err := tx.DeleteContentDocument(ctx, documentID, campaignEmailDeleteContentFence(tx, campaign.ID)); err != nil {
		return campaignContent.NormalizeContentBlockError(err)
	}
	if err := tx.DeleteCampaign(ctx, *campaign); err != nil {
		if errors.Is(err, ErrDurableReference) {
			return domainerr.Precondition("campaign delivery history or another durable reference exists").WithCause(err)
		}
		return err
	}
	return nil
}

func (s *Service) campaignDocument(ctx context.Context, c *Campaign) (*CampaignDocument, error) {
	if err := validateCampaignRecipientScope(c.RecipientScope); err != nil {
		return nil, err
	}
	var projection campaignEmailEditorProjection
	err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		projection, err = loadCampaignEmailEditorProjection(ctx, store, c.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	row := *c
	row.SourceLocale = projection.SourceLocale
	return &CampaignDocument{Campaign: row, Document: projection.Document, DocumentRevision: projection.Revision}, nil
}

// toProtoCampaignBase keeps list projections bounded. The editor fetches the
// aggregate through GetCampaign or the internal LoadDocument bootstrap.

package campaign

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// emailRegex for validating email format in test campaign sends
var campaignEmailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

func normalizeCampaignNameAndSubject(rawName, rawSubject string) (string, string, error) {
	name := strings.TrimSpace(rawName)
	subject := strings.TrimSpace(rawSubject)

	switch {
	case name == "" && subject == "":
		return "", "", errs.Required("name")
	case name == "":
		name = subject
	case subject == "":
		subject = name
	}

	return name, subject, nil
}

type CampaignService struct {
	db              *gorm.DB
	spiceDB         *auth.SpiceDBClient
	runtime         Runtime
	cdnDomain       string
	siteOrigin      string
	auditWriter     domainaudit.Appender
	contentBlocks   *contentblock.Store
	emailAuthoring  CampaignEmailAuthoringPort
	emailRendering  CampaignEmailRenderingPort
	emailDelivery   CampaignDeliveryPort
	audienceTargets CampaignAudienceTargetPort
}

type CampaignServiceOption func(*CampaignService)

func WithCampaignContentBlockStore(store *contentblock.Store) CampaignServiceOption {
	return func(service *CampaignService) {
		service.contentBlocks = store
	}
}

func WithCampaignEmailAuthoring(port CampaignEmailAuthoringPort) CampaignServiceOption {
	return func(service *CampaignService) {
		service.emailAuthoring = port
	}
}

func WithCampaignEmailRendering(port CampaignEmailRenderingPort) CampaignServiceOption {
	return func(service *CampaignService) {
		service.emailRendering = port
	}
}

func WithCampaignEmailDelivery(port CampaignDeliveryPort) CampaignServiceOption {
	return func(service *CampaignService) {
		service.emailDelivery = port
	}
}

func WithCampaignAudienceTargets(port CampaignAudienceTargetPort) CampaignServiceOption {
	return func(service *CampaignService) {
		service.audienceTargets = port
	}
}

func NewAuditedCampaignService(db *gorm.DB, runtime Runtime, cdnDomain, siteOrigin string, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient, options ...CampaignServiceOption) *CampaignService {
	if auditWriter == nil {
		panic("CampaignService: audit writer is required")
	}
	service := NewCampaignService(db, runtime, cdnDomain, siteOrigin, spiceDB, options...)
	service.auditWriter = auditWriter
	return service
}

func NewCampaignService(db *gorm.DB, runtime Runtime, cdnDomain, siteOrigin string, spiceDB *auth.SpiceDBClient, options ...CampaignServiceOption) *CampaignService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dependencycheck.MustNotNil(runtime, "Campaign runtime")
	service := &CampaignService{
		db:         db,
		spiceDB:    spiceDB,
		runtime:    runtime,
		cdnDomain:  cdnDomain,
		siteOrigin: strings.TrimRight(strings.TrimSpace(siteOrigin), "/"),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *CampaignService) requireCampaignCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) (policyv1.Can, error) {
	if canErr != nil {
		return policyv1.Can{}, errs.InvalidArgument("id", canErr.Error())
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return policyv1.Can{}, err
	}
	return can, nil
}

// GetCampaign returns a campaign by ID
func (s *CampaignService) GetCampaign(
	ctx context.Context,
	req *connect.Request[managev1.GetCampaignRequest],
) (*connect.Response[managev1.GetCampaignResponse], error) {
	can, canErr := policyv1.Campaign.View(req.Msg.Id)
	if _, err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("campaign not found")
		}
		return nil, errs.Internal(err)
	}
	protoCampaign, err := s.toProtoCampaign(ctx, &campaign)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.GetCampaignResponse{Campaign: protoCampaign}), nil
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

// ListCampaignsAdmin returns all campaigns
func (s *CampaignService) ListCampaignsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListCampaignsAdminRequest],
) (*connect.Response[managev1.ListCampaignsAdminResponse], error) {
	can, canErr := policyv1.Campaign.List()
	if _, err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).Model(&model.Campaign{})

	// Apply filters using FilterConfig
	query, err := CampaignFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = campaignSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	var campaigns []model.Campaign
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&campaigns).Error; err != nil {
		return nil, errs.Internal(err)
	}
	sourceLocales, err := loadCampaignSourceLocales(ctx, s.db, campaigns)
	if err != nil {
		return nil, err
	}

	protoCampaigns := make([]*managev1.Campaign, len(campaigns))
	for i := range campaigns {
		protoCampaigns[i], err = s.toProtoCampaignBase(&campaigns[i])
		if err != nil {
			return nil, errs.Internal(err)
		}
		protoCampaigns[i].SourceLocale = sourceLocales[campaigns[i].ID]
	}

	return connect.NewResponse(&managev1.ListCampaignsAdminResponse{
		Campaigns: protoCampaigns,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateCampaign creates a new campaign
func (s *CampaignService) CreateCampaign(
	ctx context.Context,
	req *connect.Request[managev1.CreateCampaignRequest],
) (*connect.Response[managev1.CreateCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var campaign model.Campaign
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("Campaign content Block store is not configured")
		}
		name, subject, err := normalizeCampaignNameAndSubject(req.Msg.Name, req.Msg.Subject)
		if err != nil {
			return err
		}
		sourceLocale, err := normalizeCampaignCreateSourceLocale(s.runtime, req.Msg.SourceLocale)
		if err != nil {
			return err
		}
		targetMode, segmentID, err := createCampaignTargetDefinition(req.Msg)
		if err != nil {
			return err
		}
		campaign = model.Campaign{
			Name:         name,
			Subject:      subject,
			SourceLocale: sourceLocale,
			Status:       managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			TargetMode:   targetMode,
			SegmentID:    segmentID,
		}
		if targetMode == model.CampaignTargetModeSegment {
			var segment model.AudienceSegment
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "archived_at").First(&segment, "id = ?", *segmentID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return errs.NotFound("audience_segment", *segmentID)
				}
				return errs.Internal(err)
			}
			if segment.ArchivedAt != nil {
				return errs.FailedPrecondition("archived audience segment cannot be assigned to a campaign")
			}
		}
		now := time.Now().UTC()
		campaign.CreatedAt = now
		campaign.UpdatedAt = now
		document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
			Profile:      emailContentProfile,
			SourceLocale: sourceLocale,
		})
		if err != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
		}
		documentID := document.Document.ID.String()
		campaign.ContentDocumentID = &documentID
		if err := tx.Create(&campaign).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.runtime.EnsureResourceRouteAvailableInTx(ctx, tx, "campaign", "campaigns", campaign.ID); err != nil {
			return err
		}
		if _, err := updateCampaignEmailLocaleSubject(
			ctx, tx, campaignContentEntity, campaign.ID, sourceLocale, subject, now,
		); err != nil {
			return err
		}
		touchPolicy, err := policyv1.Campaign.TouchPolicy(campaign.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.Campaign.DeletePolicy(campaign.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{touchPolicy},
			[]policyv1.RelationshipMutation{deletePolicy},
		); err != nil {
			return err
		}
		return s.appendCampaignCreatedAudit(ctx, tx, campaign.ID)
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}

	protoCampaign, err := s.toProtoCampaign(ctx, &campaign)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.CreateCampaignResponse{Campaign: protoCampaign}), nil
}

func createCampaignTargetDefinition(request *managev1.CreateCampaignRequest) (string, *string, error) {
	if request == nil {
		return "", nil, errs.InvalidArgumentMsg("campaign request is required")
	}
	switch target := request.Target.(type) {
	case *managev1.CreateCampaignRequest_All:
		if target.All == nil {
			return "", nil, errs.InvalidArgument("target", "all target is required")
		}
		return model.CampaignTargetModeAll, nil, nil
	case *managev1.CreateCampaignRequest_SegmentId:
		segmentID := strings.TrimSpace(target.SegmentId)
		if segmentID == "" {
			return "", nil, errs.InvalidArgument("target", "segment_id is required")
		}
		if _, err := uuid.Parse(segmentID); err != nil {
			return "", nil, errs.InvalidArgument("target", "segment_id must be a UUID")
		}
		return model.CampaignTargetModeSegment, &segmentID, nil
	default:
		return "", nil, errs.InvalidArgument("target", "target is required")
	}
}

func normalizeCampaignCreateSourceLocale(runtime LocaleNormalizer, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.Required("source_locale")
	}
	normalized := runtime.NormalizeSupportedLocale(value)
	if normalized == nil {
		return "", errs.InvalidArgument("source_locale", "unsupported locale")
	}
	return *normalized, nil
}

// UpdateCampaignName updates a campaign's shared admin name.
func (s *CampaignService) UpdateCampaignName(
	ctx context.Context,
	req *connect.Request[managev1.UpdateCampaignNameRequest],
) (*connect.Response[managev1.UpdateCampaignNameResponse], error) {
	campaignCan, err := policyv1.Campaign.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	var campaign model.Campaign
	var name string
	changed := false
	var now time.Time
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		name = strings.TrimSpace(req.Msg.Name)
		if name == "" {
			return errs.Required("name")
		}
		if !campaignStatusAllowsEdit(campaign.Status) {
			return errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
		}
		if campaign.Name == name {
			return nil
		}
		changed = true
		now = time.Now().UTC()
		if err := tx.Model(&campaign).
			Updates(structured.Fields{
				"name":       name,
				"updated_at": now,
			}).Error; err != nil {
			return errs.Internal(err)
		}
		return s.appendCampaignMetadataAudit(ctx, tx, campaign.ID, []string{"name"})
	}); err != nil {
		return nil, err
	}

	if changed {
		campaign.Name = name
		campaign.UpdatedAt = now
	}

	return connect.NewResponse(&managev1.UpdateCampaignNameResponse{
		Id: campaign.ID, Changed: changed, Name: campaign.Name,
		UpdatedAt: timestamppb.New(campaign.UpdatedAt),
	}), nil
}

// UpdateCampaignConfiguration owns durable recipient targeting. Collaboration
// documents must never mutate these relations because their internal RPC does
// not carry the authenticated Member actor needed for a trustworthy Audit.
func (s *CampaignService) UpdateCampaignConfiguration(
	ctx context.Context,
	req *connect.Request[managev1.UpdateCampaignConfigurationRequest],
) (*connect.Response[managev1.UpdateCampaignConfigurationResponse], error) {
	campaignCan, err := policyv1.Campaign.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}
	var campaign model.Campaign
	configurationChanged := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		observedLayoutID, lockedLayouts, err := observeAndLockCampaignLayouts(
			ctx,
			tx,
			s.emailAuthoring,
			req.Msg.Id,
			req.Msg.LayoutId,
		)
		if err != nil {
			if connectErr, ok := err.(*connect.Error); ok {
				return connectErr
			}
			return errs.Internal(err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		targetMode, err := campaignTargetModeFromProto(req.Msg.TargetMode)
		if err != nil {
			return errs.InvalidArgument("target_mode", err.Error())
		}
		recipientScope, err := campaignRecipientScopeFromProto(req.Msg.RecipientScope)
		if err != nil {
			return errs.InvalidArgument("recipient_scope", err.Error())
		}
		segmentID := nullableTrimmedString(ptrStringValue(req.Msg.SegmentId))
		layoutID := nullableTrimmedString(ptrStringValue(req.Msg.LayoutId))
		if err := validateCampaignTargetDefinition(model.Campaign{TargetMode: targetMode, SegmentID: segmentID}); err != nil {
			return errs.InvalidArgument("target_mode", err.Error())
		}
		if !campaignStatusAllowsEdit(campaign.Status) {
			return errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
		}
		if ptrStringValue(campaign.LayoutID) != observedLayoutID {
			return errs.FailedPrecondition("campaign layout assignment changed; retry")
		}
		if targetMode == model.CampaignTargetModeSegment {
			var segment model.AudienceSegment
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "archived_at").First(&segment, "id = ?", *segmentID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return errs.NotFound("audience_segment", *segmentID)
				}
				return errs.Internal(err)
			}
			if segment.ArchivedAt != nil {
				return errs.FailedPrecondition("archived audience segment cannot be assigned to a campaign")
			}
		}
		if layoutID != nil {
			if _, ok := lockedLayouts[*layoutID]; !ok {
				return errs.NotFound("email_layout", *layoutID)
			}
		}
		changed := make([]string, 0, 4)
		if campaign.TargetMode != targetMode {
			changed = append(changed, "target_mode")
		}
		if ptrStringValue(campaign.SegmentID) != ptrStringValue(segmentID) {
			changed = append(changed, "segment")
		}
		if ptrStringValue(campaign.LayoutID) != ptrStringValue(layoutID) {
			changed = append(changed, "layout")
		}
		if campaign.RecipientScope != recipientScope {
			changed = append(changed, "recipient_scope")
		}
		if len(changed) == 0 {
			return nil
		}
		configurationChanged = true
		now := time.Now().UTC()
		if err := tx.Model(&campaign).Updates(structured.Fields{"target_mode": targetMode, "segment_id": segmentID, "layout_id": layoutID, "recipient_scope": recipientScope, "updated_at": now}).Error; err != nil {
			return errs.Internal(err)
		}
		campaign.TargetMode, campaign.SegmentID, campaign.LayoutID, campaign.RecipientScope, campaign.UpdatedAt = targetMode, segmentID, layoutID, recipientScope, now
		return s.appendCampaignMetadataAudit(ctx, tx, campaign.ID, changed)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.UpdateCampaignConfigurationResponse{
		Id: campaign.ID, Changed: configurationChanged,
		TargetMode: req.Msg.TargetMode, SegmentId: campaign.SegmentID, LayoutId: campaign.LayoutID,
		RecipientScope: req.Msg.RecipientScope, UpdatedAt: timestamppb.New(campaign.UpdatedAt),
	}), nil
}

func observeAndLockCampaignLayouts(
	ctx context.Context,
	tx *gorm.DB,
	emailAuthoring CampaignEmailAuthoringPort,
	campaignID string,
	requestedLayoutID *string,
) (string, map[string]CampaignLayoutReference, error) {
	var observed struct {
		LayoutID *string `gorm:"column:layout_id"`
	}
	if err := tx.WithContext(ctx).
		Model(&model.Campaign{}).
		Select("layout_id").
		First(&observed, "id = ?", campaignID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil, errs.NotFoundMsg("campaign not found")
		}
		return "", nil, err
	}
	observedLayoutID := strings.TrimSpace(ptrStringValue(observed.LayoutID))
	if observedLayoutID == "" && strings.TrimSpace(ptrStringValue(requestedLayoutID)) == "" {
		return observedLayoutID, map[string]CampaignLayoutReference{}, nil
	}
	if emailAuthoring == nil {
		return "", nil, errs.DependencyUnavailable("Email Authoring")
	}
	lockedLayouts, err := emailAuthoring.LockLayoutsForCampaign(
		ctx,
		tx,
		observedLayoutID,
		ptrStringValue(requestedLayoutID),
	)
	return observedLayoutID, lockedLayouts, err
}

// DeleteCampaign deletes a campaign
func (s *CampaignService) DeleteCampaign(
	ctx context.Context,
	req *connect.Request[managev1.DeleteCampaignRequest],
) (*connect.Response[managev1.DeleteCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var campaign model.Campaign
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("Campaign content Block store is not configured")
		}
		if campaign.Status != managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String() {
			return errs.FailedPrecondition("only draft campaigns can be deleted")
		}
		if s.emailDelivery == nil {
			return errs.DependencyUnavailable("Email Delivery")
		}
		hasHistory, err := s.emailDelivery.HasHistory(ctx, tx, campaign.ID)
		if err != nil {
			return err
		}
		if hasHistory {
			return errs.FailedPrecondition(
				"campaign delivery history must be preserved",
			)
		}
		documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, campaignContentEntity, campaign.ID)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			campaignEmailDeleteContentFence(campaignContentEntity, campaign.ID),
		); err != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
		}
		if err := tx.Delete(&campaign).Error; err != nil {
			if dberrors.IsForeignKeyViolation(err) {
				return errs.FailedPrecondition(
					"campaign delivery history or another durable reference exists",
				)
			}
			return errs.Internal(err)
		}
		deletePolicy, err := policyv1.Campaign.DeletePolicy(campaign.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.Campaign.TouchPolicy(campaign.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{deletePolicy},
			[]policyv1.RelationshipMutation{touchPolicy},
		); err != nil {
			return err
		}
		return s.appendCampaignDeletedAudit(ctx, tx, campaign.ID)
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.DeleteCampaignResponse{
		Success: true,
	}), nil
}

// ScheduleCampaign schedules a campaign to be sent
func (s *CampaignService) ScheduleCampaign(
	ctx context.Context,
	req *connect.Request[managev1.ScheduleCampaignRequest],
) (*connect.Response[managev1.ScheduleCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	var campaign model.Campaign
	var scheduledAt time.Time

	// Use transaction with row-level locking to prevent race conditions
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the row for update to prevent concurrent modifications
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		if req.Msg.ScheduledAt == nil {
			return errs.Required("scheduled_at")
		}
		scheduledAt = req.Msg.ScheduledAt.AsTime()
		if scheduledAt.Before(time.Now()) {
			return errs.InvalidArgument("scheduled_at", "must be in the future")
		}

		if !campaignStatusAllowsSchedule(campaign.Status) {
			return errs.FailedPrecondition(errs.MsgCampaignOnlyScheduleDraft)
		}

		if err := requireCurrentCampaignRenderableContent(ctx, tx, campaign.ID); err != nil {
			return err
		}
		recipientScope, err := campaignRecipientScopeFromProto(req.Msg.GetRecipientScope())
		if err != nil {
			return errs.InvalidArgument("recipient_scope", err.Error())
		}
		if campaign.RecipientScope != recipientScope {
			return errs.FailedPrecondition("campaign recipient scope changed; reload before scheduling")
		}
		if _, err := deriveCampaignDeliveryTarget(ctx, tx, campaign, s.audienceTargets); err != nil {
			return err
		}

		campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
		campaign.ScheduledAt = &scheduledAt

		if err := tx.Save(&campaign).Error; err != nil {
			return errs.Internal(err)
		}

		_, err = createCampaignDeliveryRun(
			ctx,
			tx,
			campaign,
			scheduledAt,
			0,
			s.audienceTargets,
			s.contentBlocks,
			s.emailAuthoring,
			s.emailDelivery,
		)
		if err != nil {
			return err
		}
		return s.appendCampaignScheduleAudit(ctx, tx, campaign.ID, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStateScheduled, scheduledAt)
	})

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.ScheduleCampaignResponse{
		Id: campaign.ID, Changed: true, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED,
		ScheduledAt: timestamppb.New(*campaign.ScheduledAt), RecipientScope: req.Msg.GetRecipientScope(),
		UpdatedAt: timestamppb.New(campaign.UpdatedAt),
	}), nil
}

// CancelCampaign cancels a scheduled campaign
func (s *CampaignService) CancelCampaign(
	ctx context.Context,
	req *connect.Request[managev1.CancelCampaignRequest],
) (*connect.Response[managev1.CancelCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign

	// Use transaction with row-level locking to prevent race conditions
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the row for update to prevent concurrent modifications
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}

		// Can only cancel scheduled campaigns
		if campaign.Status != managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String() {
			return errs.FailedPrecondition(errs.MsgCampaignOnlyCancelScheduled)
		}

		campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()
		campaign.ScheduledAt = nil

		if err := tx.Save(&campaign).Error; err != nil {
			return errs.Internal(err)
		}

		if s.emailDelivery == nil {
			return errs.DependencyUnavailable("Email Delivery")
		}
		if err := s.emailDelivery.CancelActiveRuns(ctx, tx, campaign.ID, time.Now().UTC()); err != nil {
			return err
		}
		return s.appendCampaignStatusAudit(ctx, tx, campaign.ID, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft)
	})

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.CancelCampaignResponse{
		Id: campaign.ID, Changed: true, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT,
		UpdatedAt: timestamppb.New(campaign.UpdatedAt),
	}), nil
}

// SendCampaignNow immediately sends a campaign
func (s *CampaignService) SendCampaignNow(
	ctx context.Context,
	req *connect.Request[managev1.SendCampaignNowRequest],
) (*connect.Response[managev1.SendCampaignNowResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var result campaignSendNowResult
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		prepared, err := prepareCampaignSendNowWithDB(
			ctx,
			tx,
			s.spiceDB,
			campaignCan,
			s.audienceTargets,
			s.contentBlocks,
			s.emailAuthoring,
			s.emailDelivery,
			req.Msg,
		)
		result = prepared
		if err != nil {
			return err
		}
		if err := s.appendCampaignDeliveryRunAudit(ctx, tx, prepared.campaign.ID, campaignAuditState(prepared.previousStatus), sharedtelemetry.AuditStateSending, prepared.run.ID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.SendCampaignNowResponse{
		Id: result.campaign.ID, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING,
		RecipientCount: int32(result.recipientCount), UpdatedAt: timestamppb.New(result.campaign.UpdatedAt),
	}), nil
}

type campaignSendNowResult struct {
	campaign       model.Campaign
	recipientCount int64
	run            CampaignDeliveryRunRef
	previousStatus string
}

func prepareCampaignSendNowWithDB(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	campaignCan policyv1.Can,
	audienceTargets CampaignAudienceTargetPort,
	contentBlocks *contentblock.Store,
	emailAuthoring CampaignEmailAuthoringPort,
	emailDelivery CampaignDeliveryPort,
	request *managev1.SendCampaignNowRequest,
) (campaignSendNowResult, error) {
	campaign, err := lockCampaignForSendNow(ctx, tx, request.Id)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	if err := identitystate.RequireFreshAdminCan(ctx, tx, spiceDB, campaignCan); err != nil {
		return campaignSendNowResult{}, err
	}
	recipientScope, err := campaignRecipientScopeFromProto(request.GetRecipientScope())
	if err != nil {
		return campaignSendNowResult{}, errs.InvalidArgument("recipient_scope", err.Error())
	}
	previousStatus := campaign.Status
	if campaign.RecipientScope != recipientScope {
		return campaignSendNowResult{}, errs.FailedPrecondition("campaign recipient scope changed; reload before sending")
	}
	recipientCount, err := countCampaignSendNowRecipients(
		ctx,
		tx,
		audienceTargets,
		emailDelivery,
		campaign,
	)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	if recipientCount == 0 {
		return campaignSendNowResult{}, errs.FailedPrecondition(errs.MsgCampaignNoRecipients)
	}
	run, err := startCampaignSendNowWithDB(
		ctx,
		tx,
		audienceTargets,
		contentBlocks,
		emailAuthoring,
		emailDelivery,
		&campaign,
		recipientCount,
		time.Now(),
	)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	return campaignSendNowResult{campaign: campaign, recipientCount: recipientCount, run: run, previousStatus: previousStatus}, nil
}

func lockCampaignForSendNow(ctx context.Context, tx *gorm.DB, campaignID string) (model.Campaign, error) {
	var campaign model.Campaign
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ?", campaignID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.Campaign{}, errs.NotFoundMsg("campaign not found")
		}
		return model.Campaign{}, errs.Internal(err)
	}
	if !campaignStatusAllowsSendNow(campaign.Status) {
		return model.Campaign{}, errs.FailedPrecondition(errs.MsgCampaignAlreadySent)
	}
	if err := requireCurrentCampaignRenderableContent(ctx, tx, campaign.ID); err != nil {
		return model.Campaign{}, err
	}
	return campaign, nil
}

func requireCurrentCampaignRenderableContent(ctx context.Context, db *gorm.DB, campaignID string) error {
	var count int64
	if err := db.WithContext(ctx).
		Table("campaign_translation AS translation").
		Joins("JOIN campaign AS source ON source.id = translation.entity_id AND source.source_locale = translation.locale").
		Where("translation.entity_id = ?", campaignID).
		Where("translation.content_html IS NOT NULL AND btrim(translation.content_html) <> ''").
		Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count != 1 {
		return errs.FailedPrecondition(errs.MsgCampaignNeedsContent)
	}
	return nil
}

func countCampaignSendNowRecipients(
	ctx context.Context,
	tx *gorm.DB,
	audienceTargets CampaignAudienceTargetPort,
	emailDelivery CampaignDeliveryPort,
	campaign model.Campaign,
) (int64, error) {
	target, err := deriveCampaignDeliveryTarget(ctx, tx, campaign, audienceTargets)
	if err != nil {
		return 0, err
	}
	if emailDelivery == nil {
		return 0, errs.DependencyUnavailable("Email Delivery")
	}
	return emailDelivery.CountRecipients(ctx, tx, target)
}

func startCampaignSendNowWithDB(
	ctx context.Context,
	tx *gorm.DB,
	audienceTargets CampaignAudienceTargetPort,
	contentBlocks *contentblock.Store,
	emailAuthoring CampaignEmailAuthoringPort,
	emailDelivery CampaignDeliveryPort,
	campaign *model.Campaign,
	recipientCount int64,
	now time.Time,
) (CampaignDeliveryRunRef, error) {
	if emailDelivery == nil {
		return CampaignDeliveryRunRef{}, errs.DependencyUnavailable("Email Delivery")
	}
	campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String()
	campaign.ScheduledAt = nil
	campaign.SentAt = nil
	campaign.SentCount = 0
	if err := tx.Save(campaign).Error; err != nil {
		return CampaignDeliveryRunRef{}, errs.Internal(err)
	}
	if err := emailDelivery.CancelActiveRuns(ctx, tx, campaign.ID, now.UTC()); err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	run, err := createCampaignDeliveryRun(
		ctx,
		tx,
		*campaign,
		now,
		recipientCount,
		audienceTargets,
		contentBlocks,
		emailAuthoring,
		emailDelivery,
	)
	if err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	return run, nil
}

// PreviewCampaign returns a preview of the campaign email
func (s *CampaignService) PreviewCampaign(
	ctx context.Context,
	req *connect.Request[managev1.PreviewCampaignRequest],
) (*connect.Response[managev1.PreviewCampaignResponse], error) {
	can, canErr := policyv1.Campaign.View(req.Msg.Id)
	if _, err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("campaign not found")
		}
		return nil, errs.Internal(err)
	}
	// Site branding variables come from site settings. Campaign preview uses a
	// deterministic sample recipient; recipient-specific previews are not part
	// of the campaign contract.
	requestedLocale, err := resolveCampaignRequestedLocale(s.runtime, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	if s.emailRendering == nil {
		return nil, errs.DependencyUnavailable("Email rendering")
	}
	sampleData := s.emailRendering.BuildRenderData(ctx, s.db, s.cdnDomain, s.siteOrigin, requestedLocale, map[string]string{
		"recipient_email":  "john@example.com",
		"unsubscribe_link": "https://example.com/unsubscribe/sample",
	})

	localizedCampaign, _, err := ResolveLocalizedCampaign(
		ctx,
		s.db,
		s.contentBlocks,
		campaign,
		requestedLocale,
	)
	if err != nil {
		return nil, err
	}
	if req.Msg.Subject != nil {
		localizedCampaign.Subject = *req.Msg.Subject
	}
	if req.Msg.GetDocument() != nil {
		materialized, materializeErr := contentblock.MaterializeLocalizedRichTextDocument(ctx, req.Msg.GetDocument(), nil)
		if materializeErr != nil {
			return nil, errs.InvalidArgument("document", materializeErr.Error())
		}
		localizedCampaign.ContentHTML = &materialized.HTML
	}

	// Render subject and content with variables
	subject := s.emailRendering.RenderVariables(localizedCampaign.Subject, sampleData)
	htmlContent := ""
	if localizedCampaign.ContentHTML != nil {
		htmlContent = s.emailRendering.RenderVariables(*localizedCampaign.ContentHTML, sampleData)
	}

	// Add subject to sampleData for layout rendering
	sampleData["subject"] = subject

	// Determine layout ID: request param takes precedence over saved value
	var layoutID *string
	if req.Msg.LayoutId != nil {
		if strings.TrimSpace(*req.Msg.LayoutId) != "" {
			layoutID = req.Msg.LayoutId
		}
	} else {
		layoutID = localizedCampaign.LayoutID
	}

	// Wrap with layout if configured
	if layoutID != nil {
		htmlContent, err = s.emailRendering.WrapWithLayout(
			ctx,
			s.db,
			*layoutID,
			requestedLocale,
			htmlContent,
			sampleData,
		)
		if err != nil {
			return nil, err
		}
	}

	htmlContent = s.emailRendering.NormalizeHTML(htmlContent)

	// Generate plain text from HTML
	textContent := s.emailRendering.PlainText(htmlContent)

	return connect.NewResponse(&managev1.PreviewCampaignResponse{
		Preview: &managev1.CampaignPreview{
			Subject:     subject,
			HtmlContent: htmlContent,
			TextContent: textContent,
		},
	}), nil
}

// SendTestCampaign sends a test email
func (s *CampaignService) SendTestCampaign(
	ctx context.Context,
	req *connect.Request[managev1.SendTestCampaignRequest],
) (*connect.Response[managev1.SendTestCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var campaign model.Campaign
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		if req.Msg.Email == "" {
			return errs.Required("email")
		}
		if !campaignEmailRegex.MatchString(req.Msg.Email) {
			return errs.InvalidArgument("email", "invalid format")
		}
		if s.emailRendering == nil {
			return errs.DependencyUnavailable("Email rendering")
		}
		campaignID := req.Msg.Id
		sendJob := &managev1.SendEmailEvent{
			Recipient:    req.Msg.Email,
			TemplateType: fmt.Sprintf("campaign:%s", campaignID),
			TemplateData: map[string]string{
				"recipient_email":  req.Msg.Email,
				"unsubscribe_link": campaignPreviewUnsubscribeLink(s.siteOrigin),
			},
			ReferenceId:      &campaignID,
			RecipientContext: s.emailRendering.TestRecipientContext(campaignEmailTestActorID(ctx)),
		}
		if req.Msg.Locale != nil && strings.TrimSpace(*req.Msg.Locale) != "" {
			normalizedLocale := s.runtime.NormalizeSupportedLocale(*req.Msg.Locale)
			if normalizedLocale == nil {
				return errs.InvalidArgument("locale", "unsupported locale")
			}
			sendJob.Locale = normalizedLocale
		}
		messageID := "campaign-test:" + uuid.NewString()
		sendJob.MessageId = &messageID
		if err := publishCampaignDurableProtoInTransaction(ctx, s.runtime, tx, eventpkg.QueueEmailSend, messageID, sendJob); err != nil {
			return errs.Internal(fmt.Errorf("queue campaign test email: %w", err))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.SendTestCampaignResponse{
		Success: true,
	}), nil
}

func campaignPreviewUnsubscribeLink(siteOrigin string) string {
	siteOrigin = strings.TrimRight(strings.TrimSpace(siteOrigin), "/")
	if siteOrigin == "" {
		return "https://example.com/unsubscribe/sample"
	}
	return siteOrigin + "/unsubscribe?preview=1"
}

// GetCampaignStats returns statistics for a sent campaign
func (s *CampaignService) GetCampaignStats(
	ctx context.Context,
	req *connect.Request[managev1.GetCampaignStatsRequest],
) (*connect.Response[managev1.GetCampaignStatsResponse], error) {
	can, canErr := policyv1.Campaign.View(req.Msg.Id)
	if _, err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("campaign not found")
		}
		return nil, errs.Internal(err)
	}

	stats := &managev1.CampaignStats{
		TotalSent: int32(campaign.SentCount),
	}

	if s.emailDelivery == nil {
		return nil, errs.DependencyUnavailable("Email Delivery")
	}
	deliveryStats, err := s.emailDelivery.LatestStats(ctx, s.db, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if deliveryStats != nil {
		stats.TotalSent = deliveryStats.TotalSent
		stats.TotalSkipped = deliveryStats.Skipped
		stats.TotalFailed = deliveryStats.Failed
		stats.TotalBlocked = deliveryStats.Blocked
		stats.TotalSuppressed = deliveryStats.Suppressed
	}

	return connect.NewResponse(&managev1.GetCampaignStatsResponse{
		Stats: stats,
	}), nil
}

// GetCampaignRecipients returns the recipients of a campaign
func (s *CampaignService) GetCampaignRecipients(
	ctx context.Context,
	req *connect.Request[managev1.GetCampaignRecipientsRequest],
) (*connect.Response[managev1.GetCampaignRecipientsResponse], error) {
	can, canErr := policyv1.Campaign.Manage(req.Msg.Id)
	if _, err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := int(req.Msg.Offset)

	if s.emailDelivery == nil {
		return nil, errs.DependencyUnavailable("Email Delivery")
	}
	page, err := s.emailDelivery.ListRecipients(
		ctx,
		s.db,
		req.Msg.Id,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	if page.Total == 0 && len(page.Recipients) == 0 {
		return connect.NewResponse(&managev1.GetCampaignRecipientsResponse{}), nil
	}

	recipients := make([]*managev1.CampaignRecipient, len(page.Recipients))
	for i, row := range page.Recipients {
		recipient := &managev1.CampaignRecipient{
			Email:    row.Email,
			Status:   row.Status,
			MemberId: strings.TrimSpace(row.MemberID),
		}
		if row.TerminalAt != nil {
			recipient.TerminalAt = timestamppb.New(*row.TerminalAt)
			if row.Status == CampaignDeliveryRecipientStatusSent ||
				row.Status == CampaignDeliveryRecipientStatusDelivered {
				recipient.SentAt = timestamppb.New(*row.TerminalAt)
			}
		}
		if row.ErrorType != nil {
			recipient.ErrorType = row.ErrorType
		}

		recipients[i] = recipient
	}
	if err := s.runtime.AppendCampaignRecipientsAccess(ctx, req.Msg.Id); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.GetCampaignRecipientsResponse{
		Recipients: recipients,
		Total:      int32(page.Total),
	}), nil
}

func resolveCampaignRequestedLocale(runtime LocaleNormalizer, explicitLocale *string) (string, error) {
	if explicitLocale != nil && strings.TrimSpace(*explicitLocale) != "" {
		normalized := runtime.NormalizeSupportedLocale(*explicitLocale)
		if normalized == nil {
			return "", errs.InvalidArgument("locale", "unsupported locale")
		}
		return *normalized, nil
	}
	return "", nil
}

func (s *CampaignService) toProtoCampaign(ctx context.Context, c *model.Campaign) (*managev1.Campaign, error) {
	campaign, err := s.toProtoCampaignBase(c)
	if err != nil {
		return nil, err
	}
	projection, err := loadCampaignEmailEditorProjection(ctx, s.db, s.contentBlocks, campaignContentEntity, c.ID)
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
func (s *CampaignService) toProtoCampaignBase(c *model.Campaign) (*managev1.Campaign, error) {
	recipientScope, err := campaignRecipientScopeToProto(c.RecipientScope)
	if err != nil {
		return nil, err
	}
	campaign := &managev1.Campaign{
		Id:         c.ID,
		Name:       c.Name,
		Subject:    c.Subject,
		Status:     managev1.CampaignStatus(managev1.CampaignStatus_value[c.Status]),
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

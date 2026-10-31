package campaign

import (
	"context"
	"errors"
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
	"github.com/echovisionlab/geul-api/internal/dbtime"
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
)

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
) error {
	if canErr != nil {
		return errs.InvalidArgument("id", canErr.Error())
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// GetCampaign returns a campaign by ID
func (s *CampaignService) GetCampaign(
	ctx context.Context,
	req *connect.Request[managev1.GetCampaignRequest],
) (*connect.Response[managev1.GetCampaignResponse], error) {
	can, canErr := policyv1.Campaign.View(req.Msg.Id)
	if err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
	if err := s.requireCampaignCan(ctx, can, canErr); err != nil {
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
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "archived_at").First(&segment, "id = ?", *segmentID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("audience_segment", *segmentID)
				}
				return errs.Internal(err)
			}
			if segment.ArchivedAt != nil {
				return errs.FailedPrecondition("archived audience segment cannot be assigned to a campaign")
			}
		}
		now := dbtime.Now()
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
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
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
		now = dbtime.Now()
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
			return errs.Internal(err)
		}
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		targetMode := campaign.TargetMode
		segmentID := campaign.SegmentID
		recipientScope := campaign.RecipientScope
		layoutID := campaign.LayoutID
		touched := false
		if req.Msg.TargetMode != nil {
			parsedMode, err := campaignTargetModeFromProto(*req.Msg.TargetMode)
			if err != nil {
				return errs.InvalidArgument("target_mode", err.Error())
			}
			targetMode = parsedMode
			// Target mode and segment are one patch unit. An absent/null segment
			// clears the relation for ALL and fails validation for SEGMENT.
			segmentID = nullableTrimmedString(ptrStringValue(req.Msg.SegmentId))
			touched = true
		} else if req.Msg.SegmentId != nil {
			return errs.InvalidArgument("segment_id", "must be updated together with target_mode")
		}
		if req.Msg.RecipientScope != nil {
			parsedScope, err := campaignRecipientScopeFromProto(*req.Msg.RecipientScope)
			if err != nil {
				return errs.InvalidArgument("recipient_scope", err.Error())
			}
			recipientScope = parsedScope
			touched = true
		}
		if req.Msg.LayoutId != nil {
			layoutID = nullableTrimmedString(ptrStringValue(req.Msg.LayoutId))
			touched = true
		}
		if !touched {
			return errs.InvalidArgument("configuration", "must include at least one configuration field")
		}
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
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "archived_at").First(&segment, "id = ?", *segmentID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("audience_segment", *segmentID)
				}
				return errs.Internal(err)
			}
			if segment.ArchivedAt != nil {
				return errs.FailedPrecondition("archived audience segment cannot be assigned to a campaign")
			}
		}
		if req.Msg.LayoutId != nil && layoutID != nil {
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
		now := dbtime.Now()
		updates := structured.Fields{"updated_at": now}
		if campaign.TargetMode != targetMode {
			updates["target_mode"] = targetMode
		}
		if ptrStringValue(campaign.SegmentID) != ptrStringValue(segmentID) {
			updates["segment_id"] = segmentID
		}
		if ptrStringValue(campaign.LayoutID) != ptrStringValue(layoutID) {
			updates["layout_id"] = layoutID
		}
		if campaign.RecipientScope != recipientScope {
			updates["recipient_scope"] = recipientScope
		}
		if err := tx.Model(&campaign).Updates(updates).Error; err != nil {
			return errs.Internal(err)
		}
		campaign.TargetMode, campaign.SegmentID, campaign.LayoutID, campaign.RecipientScope, campaign.UpdatedAt = targetMode, segmentID, layoutID, recipientScope, now
		return s.appendCampaignMetadataAudit(ctx, tx, campaign.ID, changed)
	}); err != nil {
		return nil, err
	}
	recipientScopeProto, err := campaignRecipientScopeToProto(campaign.RecipientScope)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.UpdateCampaignConfigurationResponse{
		Id: campaign.ID, Changed: configurationChanged,
		TargetMode: campaignTargetModeToProto(campaign.TargetMode), SegmentId: campaign.SegmentID, LayoutId: campaign.LayoutID,
		RecipientScope: recipientScopeProto, UpdatedAt: timestamppb.New(campaign.UpdatedAt),
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
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
				return errs.WithCause(errs.FailedPrecondition(
					"campaign delivery history or another durable reference exists",
				), err)
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

package emailauthoring

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// layoutKeyRegex validates the format of layout keys
var layoutKeyRegex = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// emailLayoutSortConfig defines allowed sort fields for email layouts
var emailLayoutSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"key":        "key",
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "name ASC, id ASC",
}

func lockEmailLayoutsForRelationMutation(
	ctx context.Context,
	tx *gorm.DB,
	layoutIDs ...string,
) (map[string]model.EmailLayout, error) {
	unique := make(map[string]struct{}, len(layoutIDs))
	for _, layoutID := range layoutIDs {
		layoutID = strings.TrimSpace(layoutID)
		if layoutID != "" {
			unique[layoutID] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return map[string]model.EmailLayout{}, nil
	}
	normalized := make([]string, 0, len(unique))
	for layoutID := range unique {
		normalized = append(normalized, layoutID)
	}
	sort.Strings(normalized)

	var layouts []model.EmailLayout
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id IN ?", normalized).
		Order("id ASC").
		Find(&layouts).Error; err != nil {
		return nil, err
	}
	locked := make(map[string]model.EmailLayout, len(layouts))
	for _, layout := range layouts {
		locked[layout.ID] = layout
	}
	return locked, nil
}

// EmailLayoutService implements the EmailLayoutService Connect handler
type EmailLayoutService struct {
	managev1connect.UnimplementedEmailLayoutServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	cdnDomain     string
	siteOrigin    string
	auditWriter   domainaudit.Appender
	renderData    EmailRenderDataBuilder
	references    CampaignDeliveryReferences
	contentBlocks *contentblock.Store
}

// EmailRenderDataBuilder is the delivery-runtime boundary used only to render
// an admin preview. Email delivery owns the site-default projection itself.
type EmailRenderDataBuilder interface {
	BuildEmailRenderData(context.Context, *gorm.DB, string, string, string, map[string]string) map[string]string
}

type EmailLayoutServiceOption func(*EmailLayoutService)

func WithEmailLayoutRenderDataBuilder(builder EmailRenderDataBuilder) EmailLayoutServiceOption {
	return func(service *EmailLayoutService) { service.renderData = builder }
}

func WithEmailLayoutCampaignDeliveryReferences(references CampaignDeliveryReferences) EmailLayoutServiceOption {
	return func(service *EmailLayoutService) { service.references = references }
}

func WithEmailLayoutContentBlockStore(store *contentblock.Store) EmailLayoutServiceOption {
	return func(service *EmailLayoutService) { service.contentBlocks = store }
}

// NewAuditedEmailLayoutService creates an EmailLayoutService whose
// authoritative authoring mutations append Domain Audit in the same database
// transaction.
func NewAuditedEmailLayoutService(db *gorm.DB, cdnDomain, siteOrigin string, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient, options ...EmailLayoutServiceOption) *EmailLayoutService {
	if auditWriter == nil {
		panic("email layout audit writer is required")
	}
	service := NewEmailLayoutService(db, cdnDomain, siteOrigin, spiceDB, options...)
	service.auditWriter = auditWriter
	return service
}

type emailLayoutBaseRow struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID uuid.UUID `gorm:"column:content_document_id;type:uuid;not null"`
	SourceLocale      string    `gorm:"column:source_locale;type:text;not null"`
	Name              string    `gorm:"column:name;type:varchar(255);not null"`
	Key               string    `gorm:"column:key;type:varchar(100);unique;not null"`
	CreatedAt         time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (emailLayoutBaseRow) TableName() string {
	return "email_layout"
}

func createEmailLayoutBaseRow(ctx context.Context, tx *gorm.DB, layout *model.EmailLayout) error {
	baseRow := &emailLayoutBaseRow{
		ContentDocumentID: uuid.MustParse(layout.ContentDocumentID),
		SourceLocale:      layout.SourceLocale,
		Name:              layout.Name,
		Key:               layout.Key,
		CreatedAt:         layout.CreatedAt,
		UpdatedAt:         layout.UpdatedAt,
	}

	if err := tx.WithContext(ctx).
		Omit("ID").
		Clauses(clause.Returning{}).
		Create(baseRow).Error; err != nil {
		return err
	}

	layout.ID = baseRow.ID
	layout.CreatedAt = baseRow.CreatedAt
	layout.UpdatedAt = baseRow.UpdatedAt
	return nil
}

func overlayEmailLayoutCanonicalSourceContent(
	ctx context.Context,
	db *gorm.DB,
	layout *model.EmailLayout,
) error {
	if layout == nil {
		return nil
	}

	sourceLocale, found, err := email.ResolveLayoutTranslationSourceLocale(ctx, db, layout.ID)
	if err != nil {
		return err
	}
	if !found {
		return errs.FailedPrecondition("Email Layout source locale is not initialized")
	}
	state, err := email.LoadCanonicalLayoutTranslationDocument(ctx, db, layout.ID, sourceLocale)
	if err != nil {
		return err
	}

	layout.HTMLContent = derefString(state.ContentHTML)
	return nil
}

func persistNewEmailLayoutCanonicalSource(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
	sourceLocale string,
	htmlContent string,
	now time.Time,
) error {
	normalizedHTMLContent := email.NormalizeTemplatePlaceholders(htmlContent)
	canonicalHTMLContent, err := email.CanonicalizeLayoutSourceMarkers(normalizedHTMLContent)
	if err != nil {
		return errs.InvalidArgument("html_content", err.Error())
	}
	normalizedHTMLContent = canonicalHTMLContent
	contentText := email.StripHTML(normalizedHTMLContent)
	return email.SaveLayoutSourceLocaleDocument(
		ctx,
		tx,
		layoutID,
		sourceLocale,
		email.LayoutTranslationDocument{
			ContentHTML: &normalizedHTMLContent,
			ContentText: &contentText,
		},
		now,
	)
}

// NewEmailLayoutService creates a new EmailLayoutService
func NewEmailLayoutService(db *gorm.DB, cdnDomain, siteOrigin string, spiceDB *auth.SpiceDBClient, options ...EmailLayoutServiceOption) *EmailLayoutService {
	if db == nil {
		panic("db is required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	service := &EmailLayoutService{
		db:         db,
		spiceDB:    spiceDB,
		cdnDomain:  cdnDomain,
		siteOrigin: strings.TrimRight(strings.TrimSpace(siteOrigin), "/"),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *EmailLayoutService) requireEmailLayoutCan(
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

// GetEmailLayout retrieves an email layout by ID (admin only)
func (s *EmailLayoutService) GetEmailLayout(
	ctx context.Context,
	req *connect.Request[managev1.GetEmailLayoutRequest],
) (*connect.Response[managev1.EmailLayout], error) {
	can, canErr := policyv1.EmailLayout.View(req.Msg.Id)
	if _, err := s.requireEmailLayoutCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	var layout model.EmailLayout
	if err := s.db.WithContext(ctx).First(&layout, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("email layout", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	if err := overlayEmailLayoutCanonicalSourceContent(ctx, s.db, &layout); err != nil {
		return nil, err
	}
	if err := loadEmailLayoutReferenceCounts(
		ctx,
		s.db,
		s.references,
		[]*model.EmailLayout{&layout},
	); err != nil {
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(toProtoEmailLayout(&layout)), nil
}

// ListEmailLayoutsAdmin returns a paginated list of email layouts (admin only)
func (s *EmailLayoutService) ListEmailLayoutsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListEmailLayoutsAdminRequest],
) (*connect.Response[managev1.ListEmailLayoutsAdminResponse], error) {
	can, canErr := policyv1.EmailLayout.List()
	if _, err := s.requireEmailLayoutCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	var layouts []model.EmailLayout
	var total int64

	query := s.db.WithContext(ctx).Model(&model.EmailLayout{})

	// Apply filters
	query, err := EmailLayoutFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total after filters
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply sorting
	query, err = emailLayoutSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// Apply pagination
	pg := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)

	if err := pg.Apply(query).Find(&layouts).Error; err != nil {
		return nil, errs.Internal(err)
	}
	layoutPointers := make([]*model.EmailLayout, 0, len(layouts))
	for i := range layouts {
		layoutPointers = append(layoutPointers, &layouts[i])
	}
	if err := loadEmailLayoutReferenceCounts(ctx, s.db, s.references, layoutPointers); err != nil {
		return nil, errs.Internal(err)
	}
	if err := overlayCanonicalEmailLayoutSourcesForList(ctx, s.db, layouts); err != nil {
		return nil, errs.Internal(err)
	}

	protoLayouts := make([]*managev1.EmailLayout, len(layouts))
	for i := range layouts {
		protoLayouts[i] = toProtoEmailLayout(&layouts[i])
	}

	return connect.NewResponse(&managev1.ListEmailLayoutsAdminResponse{
		Layouts:    protoLayouts,
		Pagination: pg.BuildResponse(total),
	}), nil
}

func overlayCanonicalEmailLayoutSourcesForList(
	ctx context.Context,
	db *gorm.DB,
	layouts []model.EmailLayout,
) error {
	if len(layouts) == 0 {
		return nil
	}

	ids := make([]string, 0, len(layouts))
	for i := range layouts {
		ids = append(ids, layouts[i].ID)
	}
	byID, err := email.LoadCanonicalLayoutProjections(ctx, db, ids)
	if err != nil {
		return err
	}
	for i := range layouts {
		projection, ok := byID[layouts[i].ID]
		if !ok {
			return errs.NotFound("email layout source translation", layouts[i].ID)
		}
		layouts[i].HTMLContent = projection.ContentHTML
	}
	return nil
}

// validateLayoutKey validates the format of a layout key.
func validateLayoutKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if len(key) > 100 {
		return fmt.Errorf("key must be at most 100 characters")
	}
	if !layoutKeyRegex.MatchString(key) {
		return fmt.Errorf("key must start with a lowercase letter and contain only lowercase letters, numbers, underscores, and hyphens")
	}
	return nil
}

// validateLayoutName validates the format of a layout name.
func validateLayoutName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("name must be at most 255 characters")
	}
	return nil
}

// validateLayoutHTMLContent validates the HTML content of a layout.
func validateLayoutHTMLContent(content string) error {
	return email.ValidateLayoutHTMLContentError(content)
}

// CreateEmailLayout creates a new email layout (admin only)
func (s *EmailLayoutService) CreateEmailLayout(
	ctx context.Context,
	req *connect.Request[managev1.CreateEmailLayoutRequest],
) (*connect.Response[managev1.EmailLayout], error) {
	layoutCan, err := policyv1.EmailLayout.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var layout *model.EmailLayout
	var normalizedHTMLContent string
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, layoutCan); err != nil {
			return err
		}
		if err := validateLayoutKey(req.Msg.Key); err != nil {
			return errs.InvalidArgument("key", err.Error())
		}
		if err := validateLayoutName(req.Msg.Name); err != nil {
			return errs.InvalidArgument("name", err.Error())
		}
		if err := validateLayoutHTMLContent(req.Msg.HtmlContent); err != nil {
			return errs.InvalidArgument("html_content", err.Error())
		}
		normalizedHTMLContent = email.NormalizeTemplatePlaceholders(req.Msg.HtmlContent)
		canonicalHTMLContent, err := email.CanonicalizeLayoutSourceMarkers(normalizedHTMLContent)
		if err != nil {
			return errs.InvalidArgument("html_content", err.Error())
		}
		normalizedHTMLContent = canonicalHTMLContent
		if s.contentBlocks == nil {
			return errs.InternalMsg("Email Layout Content Document store is not configured")
		}
		sourceLocale, err := canonicalEmailLayoutRoomLocale(req.Msg.SourceLocale)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
			Profile: emailLayoutContentProfile, SourceLocale: sourceLocale,
		})
		if err != nil {
			return errs.Internal(err)
		}
		layout = &model.EmailLayout{
			ContentDocumentID: document.Document.ID.String(),
			SourceLocale:      sourceLocale,
			Name:              strings.TrimSpace(req.Msg.Name),
			Key:               req.Msg.Key,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := createEmailLayoutBaseRow(ctx, tx, layout); err != nil {
			if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
				return errs.AlreadyExists("email layout", "key", req.Msg.Key)
			}
			return errs.Internal(err)
		}
		if err := persistNewEmailLayoutCanonicalSource(ctx, tx, layout.ID, sourceLocale, normalizedHTMLContent, now); err != nil {
			return err
		}
		if err := appendEmailLayoutCreatedAudit(ctx, tx, s.auditWriter, layout.ID); err != nil {
			return err
		}
		touchPolicy, err := policyv1.EmailLayout.TouchPolicy(layout.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.EmailLayout.DeletePolicy(layout.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{touchPolicy},
			[]policyv1.RelationshipMutation{deletePolicy},
		)
	})
	if err != nil {
		return nil, err
	}
	layout.HTMLContent = normalizedHTMLContent
	return connect.NewResponse(toProtoEmailLayout(layout)), nil
}

// UpdateEmailLayout updates an email layout (admin only)
func (s *EmailLayoutService) UpdateEmailLayout(
	ctx context.Context,
	req *connect.Request[managev1.UpdateEmailLayoutRequest],
) (*connect.Response[managev1.EmailLayout], error) {
	layoutCan, err := policyv1.EmailLayout.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}
	updatedAt := time.Now().UTC()
	var layout model.EmailLayout
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated, err := s.updateEmailLayoutWithDB(ctx, tx, req.Msg, updatedAt, s.auditWriter, layoutCan)
		layout = updated
		return err
	}); err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(toProtoEmailLayout(&layout)), nil
}

func buildEmailLayoutUpdates(
	request *managev1.UpdateEmailLayoutRequest,
	updatedAt time.Time,
) (structured.Fields, error) {
	updates := structured.Fields{}
	if request.Name != nil {
		if err := validateLayoutName(*request.Name); err != nil {
			return nil, errs.InvalidArgument("name", err.Error())
		}
		updates["name"] = strings.TrimSpace(*request.Name)
	}
	if request.Key != nil {
		if err := validateLayoutKey(*request.Key); err != nil {
			return nil, errs.InvalidArgument("key", err.Error())
		}
		updates["key"] = *request.Key
	}
	return updates, nil
}

func (s *EmailLayoutService) updateEmailLayoutWithDB(
	ctx context.Context,
	tx *gorm.DB,
	request *managev1.UpdateEmailLayoutRequest,
	updatedAt time.Time,
	auditWriter domainaudit.Appender,
	layoutCan policyv1.Can,
) (model.EmailLayout, error) {
	layout, err := lockMutableEmailLayout(ctx, tx, s.references, request.Id)
	if err != nil {
		return model.EmailLayout{}, err
	}
	if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, layoutCan); err != nil {
		return model.EmailLayout{}, err
	}
	updates, err := buildEmailLayoutUpdates(request, updatedAt)
	if err != nil {
		return model.EmailLayout{}, err
	}
	metadataFields := emailLayoutMetadataChangedFields(layout, updates)
	if len(metadataFields) > 0 {
		updates["updated_at"] = updatedAt
		if err := applyEmailLayoutUpdates(ctx, tx, &layout, request.Key, updates); err != nil {
			return model.EmailLayout{}, err
		}
	}
	if err := appendEmailLayoutMetadataAudit(ctx, tx, auditWriter, layout.ID, metadataFields); err != nil {
		return model.EmailLayout{}, err
	}
	if err := loadEmailLayoutForResponse(ctx, tx, s.references, &layout); err != nil {
		return model.EmailLayout{}, err
	}
	return layout, nil
}

func emailLayoutMetadataChangedFields(layout model.EmailLayout, updates structured.Fields) []string {
	fields := make([]string, 0, 2)
	if value, ok := updates["name"].(string); ok && value != layout.Name {
		fields = append(fields, "name")
	} else {
		delete(updates, "name")
	}
	if value, ok := updates["key"].(string); ok && value != layout.Key {
		fields = append(fields, "key")
	} else {
		delete(updates, "key")
	}
	return fields
}

func lockMutableEmailLayout(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	layoutID string,
) (model.EmailLayout, error) {
	var layout model.EmailLayout
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&layout, "id = ?", layoutID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.EmailLayout{}, errs.NotFound("email layout", layoutID)
		}
		return model.EmailLayout{}, err
	}
	if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, references, layout.ID); err != nil {
		return model.EmailLayout{}, err
	}
	return layout, nil
}

func applyEmailLayoutUpdates(
	ctx context.Context,
	tx *gorm.DB,
	layout *model.EmailLayout,
	requestedKey *string,
	updates structured.Fields,
) error {
	if err := tx.WithContext(ctx).Model(layout).Updates(updates).Error; err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			key := layout.Key
			if requestedKey != nil {
				key = *requestedKey
			}
			return errs.AlreadyExists("email layout", "key", key)
		}
		return errs.Internal(err)
	}
	return nil
}

func loadEmailLayoutForResponse(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	layout *model.EmailLayout,
) error {
	if err := tx.First(layout, "id = ?", layout.ID).Error; err != nil {
		return err
	}
	if err := overlayEmailLayoutCanonicalSourceContent(ctx, tx, layout); err != nil {
		return err
	}
	return loadEmailLayoutReferenceCounts(ctx, tx, references, []*model.EmailLayout{layout})
}

// DeleteEmailLayout permanently deletes a layout that has no current authoring
// references or scheduled/sending work. Terminal delivery history keeps its
// immutable render snapshot and source-version metadata while detaching the
// mutable authoring-row identity.
func (s *EmailLayoutService) DeleteEmailLayout(
	ctx context.Context,
	req *connect.Request[managev1.DeleteEmailLayoutRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	layoutCan, err := policyv1.EmailLayout.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var layout model.EmailLayout
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&layout, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("email layout", req.Msg.Id)
			}
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, layoutCan); err != nil {
			return err
		}
		if err := loadEmailLayoutReferenceCounts(
			ctx,
			tx,
			s.references,
			[]*model.EmailLayout{&layout},
		); err != nil {
			return err
		}
		if layout.CampaignCount > 0 || layout.TemplateCount > 0 {
			return errs.FailedPrecondition(
				"email layout must be unreferenced by campaigns and templates before deletion",
			)
		}
		if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, s.references, layout.ID); err != nil {
			return err
		}
		if err := s.references.DetachLayoutHistory(ctx, tx, layout.ID); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("Email Layout Content Document store is not configured")
		}
		documentID, parseErr := uuid.Parse(layout.ContentDocumentID)
		if parseErr != nil || documentID == uuid.Nil {
			return errs.FailedPrecondition("Email Layout Content Document is not initialized")
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx, tx, documentID, emailLayoutContentFence(s.references, layout.ID),
		); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Delete(&layout).Error; err != nil {
			return err
		}
		if err := appendEmailLayoutDeletedAudit(ctx, tx, s.auditWriter, layout.ID); err != nil {
			return err
		}
		deletePolicy, err := policyv1.EmailLayout.DeletePolicy(layout.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.EmailLayout.TouchPolicy(layout.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{deletePolicy},
			[]policyv1.RelationshipMutation{touchPolicy},
		)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// PreviewEmailLayout renders a preview of the layout with sample content (admin only)
func (s *EmailLayoutService) PreviewEmailLayout(
	ctx context.Context,
	req *connect.Request[managev1.PreviewEmailLayoutRequest],
) (*connect.Response[managev1.PreviewEmailLayoutResponse], error) {
	can, canErr := policyv1.EmailLayout.View(req.Msg.Id)
	if _, err := s.requireEmailLayoutCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	var layout model.EmailLayout
	if err := s.db.WithContext(ctx).First(&layout, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("email layout", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	if err := overlayEmailLayoutCanonicalSourceContent(ctx, s.db, &layout); err != nil {
		return nil, err
	}

	// Use sample content or default
	sampleContent := "<p>This is sample email content that would appear inside the layout.</p>"
	if req.Msg.SampleContent != nil && *req.Msg.SampleContent != "" {
		sampleContent = *req.Msg.SampleContent
	}

	sampleData := s.sampleLayoutPreviewData(ctx, sampleContent, req.Msg.Locale)

	layoutHTML := layout.HTMLContent
	if req.Msg.Locale != nil && strings.TrimSpace(*req.Msg.Locale) != "" {
		localizedHTML, _, err := email.ResolveLocalizedEmailLayoutHTML(ctx, s.db, layout.ID, *req.Msg.Locale)
		if err != nil {
			if connectErr, ok := err.(*connect.Error); ok {
				return nil, connectErr
			}
			return nil, errs.Internal(err)
		}
		layoutHTML = localizedHTML
	}

	renderedHTML := renderLayoutPreviewHTML(layoutHTML, sampleContent, sampleData)

	return connect.NewResponse(&managev1.PreviewEmailLayoutResponse{
		Html: renderedHTML,
	}), nil
}

// PreviewEmailLayoutContent renders a preview of raw HTML content with validation (admin only)
// This is used for live preview in the editor before saving.
func (s *EmailLayoutService) PreviewEmailLayoutContent(
	ctx context.Context,
	req *connect.Request[managev1.PreviewEmailLayoutContentRequest],
) (*connect.Response[managev1.PreviewEmailLayoutContentResponse], error) {
	can, canErr := policyv1.EmailLayout.PreviewContent()
	if _, err := s.requireEmailLayoutCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	htmlContent := req.Msg.HtmlContent
	htmlContent = email.NormalizeTemplatePlaceholders(htmlContent)
	var validationErrors []*managev1.EmailLayoutValidationError

	for _, issue := range email.ValidateLayoutHTMLContent(htmlContent) {
		validationErrors = append(validationErrors, &managev1.EmailLayoutValidationError{
			Code:    issue.Code,
			Message: issue.Message,
		})
	}

	// If there are validation errors, return them
	if len(validationErrors) > 0 {
		return connect.NewResponse(&managev1.PreviewEmailLayoutContentResponse{
			Valid:  false,
			Html:   "",
			Errors: validationErrors,
		}), nil
	}

	// Use sample content or default
	sampleContent := "<h1>Welcome!</h1><p>This is sample email content that would appear inside the layout.</p><p>Thank you for subscribing.</p>"
	if req.Msg.SampleContent != nil && *req.Msg.SampleContent != "" {
		sampleContent = *req.Msg.SampleContent
	}

	sampleData := s.sampleLayoutPreviewData(ctx, sampleContent, req.Msg.Locale)

	renderedHTML := renderLayoutPreviewHTML(htmlContent, sampleContent, sampleData)

	return connect.NewResponse(&managev1.PreviewEmailLayoutContentResponse{
		Valid:  true,
		Html:   renderedHTML,
		Errors: nil,
	}), nil
}

// Helper functions

// renderLayoutPreviewHTML mirrors production layout rendering:
// 1) inject raw HTML into {{content}}, 2) render remaining escaped variables.
func renderLayoutPreviewHTML(layoutHTML, contentHTML string, data map[string]string) string {
	layoutHTML = email.NormalizeTemplatePlaceholders(layoutHTML)
	rendered := strings.ReplaceAll(layoutHTML, "{{content}}", contentHTML)
	return email.RenderVars(rendered, data)
}

func (s *EmailLayoutService) sampleLayoutPreviewData(
	ctx context.Context,
	sampleContent string,
	locale *string,
) map[string]string {
	requestedLocale := ""
	if locale != nil {
		requestedLocale = *locale
	}
	input := map[string]string{
		"content":          sampleContent,
		"subject":          "Sample Email Subject",
		"recipient_name":   "John Doe",
		"recipient_email":  "john@example.com",
		"unsubscribe_link": "https://example.com/unsubscribe/abc123",
	}
	return s.buildEmailRenderData(ctx, requestedLocale, input)
}

func (s *EmailLayoutService) buildEmailRenderData(ctx context.Context, requestedLocale string, input map[string]string) map[string]string {
	if s.renderData != nil {
		return s.renderData.BuildEmailRenderData(ctx, s.db, s.cdnDomain, s.siteOrigin, requestedLocale, input)
	}
	input["site_origin"] = s.siteOrigin
	return input
}

func toProtoEmailLayout(l *model.EmailLayout) *managev1.EmailLayout {
	proto := &managev1.EmailLayout{
		Id:               l.ID,
		Name:             l.Name,
		Key:              l.Key,
		HtmlContent:      email.NormalizeTemplatePlaceholders(l.HTMLContent),
		CreatedAt:        timestamppb.New(l.CreatedAt),
		UpdatedAt:        timestamppb.New(l.UpdatedAt),
		CampaignCount:    l.CampaignCount,
		TemplateCount:    l.TemplateCount,
		DeliveryRunCount: l.DeliveryRunCount,
		SourceLocale:     l.SourceLocale,
	}
	return proto
}

// EmailLayoutFilterConfig defines filter fields for ListEmailLayouts
var EmailLayoutFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "key"},
		},
	},
}

// ListEmailLayoutsPaginated returns layouts with proper pagination response
func (s *EmailLayoutService) ListEmailLayoutsPaginated(
	ctx context.Context,
	pg *queryutil.Pagination,
) ([]*model.EmailLayout, *commonv1.PaginationResponse, error) {
	var layouts []model.EmailLayout
	var total int64

	query := s.db.WithContext(ctx).Model(&model.EmailLayout{})

	if err := query.Count(&total).Error; err != nil {
		return nil, nil, errs.Internal(err)
	}

	query = query.Order("name ASC")

	if err := pg.Apply(query).Find(&layouts).Error; err != nil {
		return nil, nil, errs.Internal(err)
	}
	if err := overlayCanonicalEmailLayoutSourcesForList(ctx, s.db, layouts); err != nil {
		return nil, nil, errs.Internal(err)
	}

	result := make([]*model.EmailLayout, len(layouts))
	for i := range layouts {
		result[i] = &layouts[i]
	}

	return result, pg.BuildResponse(total), nil
}

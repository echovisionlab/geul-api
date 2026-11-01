package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
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

// EmailRenderDataBuilder is the delivery-runtime boundary used only to render
// an admin preview. Email delivery owns the site-default projection itself.
type EmailRenderDataBuilder interface {
	BuildEmailRenderData(context.Context, *gorm.DB, string, string, string, map[string]string) map[string]string
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

	sourceLocale, found, err := email.ResolveLayoutTranslationSourceLocale(ctx, emailpostgres.Bind(db), layout.ID)
	if err != nil {
		return err
	}
	if !found {
		return domainerr.Precondition("Email Layout source locale is not initialized")
	}
	state, err := email.LoadCanonicalLayoutTranslationDocument(ctx, emailpostgres.Bind(db), layout.ID, sourceLocale)
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
		return domainerr.InvalidField("html_content", err.Error())
	}
	normalizedHTMLContent = canonicalHTMLContent
	contentText := email.StripHTML(normalizedHTMLContent)
	return email.SaveLayoutSourceLocaleDocument(
		ctx,
		emailpostgres.Bind(tx),
		layoutID,
		sourceLocale,
		email.LayoutTranslationDocument{
			ContentHTML: &normalizedHTMLContent,
			ContentText: &contentText,
		},
		now,
	)
}

// GetEmailLayoutQuery reads one Email Layout.
type GetEmailLayoutQuery struct {
	ID string
}

// GetEmailLayout returns the layout with its source HTML and reference
// counts.
func (s *Service) GetEmailLayout(ctx context.Context, query GetEmailLayoutQuery) (*managev1.EmailLayout, error) {
	can, canErr := policyv1.EmailLayout.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	layout, err := s.loadEmailLayout(ctx, query.ID)
	if err != nil {
		return nil, err
	}
	if err := loadEmailLayoutReferenceCounts(ctx, s.db, s.references, []*model.EmailLayout{&layout}); err != nil {
		return nil, err
	}
	return toProtoEmailLayout(&layout), nil
}

// loadEmailLayout reads the layout with its canonical source HTML.
func (s *Service) loadEmailLayout(ctx context.Context, layoutID string) (model.EmailLayout, error) {
	var layout model.EmailLayout
	if err := s.db.WithContext(ctx).First(&layout, "id = ?", layoutID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return layout, domainerr.ResourceNotFound("email layout", layoutID)
		}
		return layout, err
	}
	return layout, overlayEmailLayoutCanonicalSourceContent(ctx, s.db, &layout)
}

// ListEmailLayoutsQuery pages through Email Layouts for administrators.
type ListEmailLayoutsQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// EmailLayoutPage is one page of Email Layouts.
type EmailLayoutPage struct {
	Layouts    []*managev1.EmailLayout
	Pagination *commonv1.PaginationResponse
}

// ListEmailLayouts returns one page of layouts with their source HTML and
// reference counts.
func (s *Service) ListEmailLayouts(ctx context.Context, query ListEmailLayoutsQuery) (EmailLayoutPage, error) {
	can, canErr := policyv1.EmailLayout.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return EmailLayoutPage{}, err
	}
	list, err := emailLayoutFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.EmailLayout{}), query.Filters)
	if err != nil {
		return EmailLayoutPage{}, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return EmailLayoutPage{}, err
	}
	list, err = emailLayoutSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return EmailLayoutPage{}, err
	}
	pg := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var layouts []model.EmailLayout
	if err := pg.Apply(list).Find(&layouts).Error; err != nil {
		return EmailLayoutPage{}, err
	}
	pointers := make([]*model.EmailLayout, 0, len(layouts))
	for i := range layouts {
		pointers = append(pointers, &layouts[i])
	}
	if err := loadEmailLayoutReferenceCounts(ctx, s.db, s.references, pointers); err != nil {
		return EmailLayoutPage{}, err
	}
	if err := overlayCanonicalEmailLayoutSourcesForList(ctx, s.db, layouts); err != nil {
		return EmailLayoutPage{}, err
	}
	protoLayouts := make([]*managev1.EmailLayout, len(layouts))
	for i := range layouts {
		protoLayouts[i] = toProtoEmailLayout(&layouts[i])
	}
	return EmailLayoutPage{Layouts: protoLayouts, Pagination: pg.BuildResponse(total)}, nil
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
	byID, err := email.LoadCanonicalLayoutProjections(ctx, emailpostgres.Bind(db), ids)
	if err != nil {
		return err
	}
	for i := range layouts {
		projection, ok := byID[layouts[i].ID]
		if !ok {
			// A listed layout without its canonical source row is corrupt data, not a missing
			// resource: fail closed as Internal instead of leaking NotFound for the whole list.
			return fmt.Errorf("email layout %s has no canonical source translation", layouts[i].ID)
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

// CreateEmailLayoutCommand creates an Email Layout from its source HTML.
type CreateEmailLayoutCommand struct {
	Name         string
	Key          string
	HTMLContent  string
	SourceLocale string
}

// newEmailLayout validates the command and returns the layout it creates
// with its canonical source HTML.
func newEmailLayout(command CreateEmailLayoutCommand) (*model.EmailLayout, error) {
	if err := validateLayoutKey(command.Key); err != nil {
		return nil, domainerr.InvalidField("key", err.Error())
	}
	if err := validateLayoutName(command.Name); err != nil {
		return nil, domainerr.InvalidField("name", err.Error())
	}
	if err := validateLayoutHTMLContent(command.HTMLContent); err != nil {
		return nil, domainerr.InvalidField("html_content", err.Error())
	}
	htmlContent, err := email.CanonicalizeLayoutSourceMarkers(email.NormalizeTemplatePlaceholders(command.HTMLContent))
	if err != nil {
		return nil, domainerr.InvalidField("html_content", err.Error())
	}
	sourceLocale, err := canonicalEmailLayoutRoomLocale(command.SourceLocale)
	if err != nil {
		return nil, err
	}
	now := dbtime.Now()
	return &model.EmailLayout{
		SourceLocale: sourceLocale, Name: strings.TrimSpace(command.Name), Key: command.Key,
		HTMLContent: htmlContent, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// CreateEmailLayout creates the layout with its source locale document and
// grants its policy.
func (s *Service) CreateEmailLayout(ctx context.Context, command CreateEmailLayoutCommand) (*managev1.EmailLayout, error) {
	layout, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.EmailLayout]{
		Access: mutation.FreshAdminCan(policyv1.EmailLayout.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.EmailLayout, error) {
			layout, err := newEmailLayout(command)
			if err != nil {
				return nil, err
			}
			document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
				Profile: emailLayoutContentProfile, SourceLocale: layout.SourceLocale,
			})
			if err != nil {
				return nil, err
			}
			layout.ContentDocumentID = document.Document.ID.String()
			if err := createEmailLayoutBaseRow(ctx, tx, layout); err != nil {
				if dberrors.IsUniqueViolation(err) {
					return nil, domainerr.Duplicate("email layout", "key", command.Key)
				}
				return nil, err
			}
			return layout, persistNewEmailLayoutCanonicalSource(ctx, tx, layout.ID, layout.SourceLocale, layout.HTMLContent, layout.CreatedAt)
		},
		Audit: func(layout *model.EmailLayout) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditEmailLayoutCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailLayoutCreatedAuditRecord(metadata, layout.ID)
			})
		},
		Relationships: func(layout *model.EmailLayout) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyLifecycle(policyv1.EmailLayout.TouchPolicy, policyv1.EmailLayout.DeletePolicy, layout.ID)
		},
	})
	if err != nil {
		return nil, err
	}
	return toProtoEmailLayout(layout), nil
}

// UpdateEmailLayoutCommand renames an Email Layout or changes its key; a nil
// field keeps its value.
type UpdateEmailLayoutCommand struct {
	ID   string
	Name *string
	Key  *string
}

// emailLayoutUpdate is the layout an update left and the metadata fields it
// changed.
type emailLayoutUpdate struct {
	layout model.EmailLayout
	fields []string
}

func (u emailLayoutUpdate) audit() mutation.Audit {
	if len(u.fields) == 0 {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditEmailLayoutUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutMetadataAuditRecord(metadata, u.layout.ID, u.fields)
	})
}

// UpdateEmailLayout changes the layout's name and key while no active
// delivery renders it.
func (s *Service) UpdateEmailLayout(ctx context.Context, command UpdateEmailLayoutCommand) (*managev1.EmailLayout, error) {
	access, err := adminAccess(policyv1.EmailLayout.Edit(command.ID))
	if err != nil {
		return nil, err
	}
	updatedAt := dbtime.Now()
	var layout model.EmailLayout
	update, err := mutation.Run(ctx, s.mutations, mutation.Spec[emailLayoutUpdate]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			layout, err = lockMutableEmailLayout(ctx, tx, s.references, command.ID)
			return err
		},
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (emailLayoutUpdate, error) {
			updates, err := buildEmailLayoutUpdates(command)
			if err != nil {
				return emailLayoutUpdate{}, err
			}
			update := emailLayoutUpdate{fields: emailLayoutMetadataChangedFields(layout, updates)}
			if len(update.fields) > 0 {
				updates["updated_at"] = updatedAt
				if err := applyEmailLayoutUpdates(ctx, tx, &layout, command.Key, updates); err != nil {
					return emailLayoutUpdate{}, err
				}
			}
			update.layout = layout
			return update, loadEmailLayoutForResponse(ctx, tx, s.references, &update.layout)
		},
		Audit: emailLayoutUpdate.audit,
	})
	if err != nil {
		return nil, err
	}
	return toProtoEmailLayout(&update.layout), nil
}

func buildEmailLayoutUpdates(command UpdateEmailLayoutCommand) (structured.Fields, error) {
	updates := structured.Fields{}
	if command.Name != nil {
		if err := validateLayoutName(*command.Name); err != nil {
			return nil, domainerr.InvalidField("name", err.Error())
		}
		updates["name"] = strings.TrimSpace(*command.Name)
	}
	if command.Key != nil {
		if err := validateLayoutKey(*command.Key); err != nil {
			return nil, domainerr.InvalidField("key", err.Error())
		}
		updates["key"] = *command.Key
	}
	return updates, nil
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
	layout, err := lockEmailLayout(ctx, tx, layoutID)
	if err != nil {
		return model.EmailLayout{}, err
	}
	return layout, references.RequireLayoutMutable(ctx, tx, layout.ID)
}

func lockEmailLayout(ctx context.Context, tx *gorm.DB, layoutID string) (model.EmailLayout, error) {
	var layout model.EmailLayout
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&layout, "id = ?", layoutID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.EmailLayout{}, domainerr.ResourceNotFound("email layout", layoutID)
		}
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
		if dberrors.IsUniqueViolation(err) {
			key := layout.Key
			if requestedKey != nil {
				key = *requestedKey
			}
			return domainerr.Duplicate("email layout", "key", key)
		}
		return err
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

// DeleteEmailLayoutCommand deletes an Email Layout.
type DeleteEmailLayoutCommand struct {
	ID string
}

// DeleteEmailLayout permanently deletes a layout that has no current authoring
// references or scheduled/sending work. Terminal delivery history keeps its
// immutable render snapshot and source-version metadata while detaching the
// mutable authoring-row identity.
func (s *Service) DeleteEmailLayout(ctx context.Context, command DeleteEmailLayoutCommand) error {
	access, err := adminAccess(policyv1.EmailLayout.Delete(command.ID))
	if err != nil {
		return err
	}
	var layout model.EmailLayout
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			layout, err = lockEmailLayout(ctx, tx, command.ID)
			return err
		},
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			return layout.ID, s.deleteLockedEmailLayout(ctx, tx, &layout)
		},
		Audit: func(layoutID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditEmailLayoutDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailLayoutDeletedAuditRecord(metadata, layoutID)
			})
		},
		Relationships: func(layoutID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			apply, compensate, err := policyLifecycle(policyv1.EmailLayout.TouchPolicy, policyv1.EmailLayout.DeletePolicy, layoutID)
			return compensate, apply, err
		},
	})
	return err
}

func (s *Service) deleteLockedEmailLayout(ctx context.Context, tx *gorm.DB, layout *model.EmailLayout) error {
	if err := loadEmailLayoutReferenceCounts(ctx, tx, s.references, []*model.EmailLayout{layout}); err != nil {
		return err
	}
	if layout.CampaignCount > 0 || layout.TemplateCount > 0 {
		return domainerr.Precondition("email layout must be unreferenced by campaigns and templates before deletion")
	}
	if err := s.references.RequireLayoutMutable(ctx, tx, layout.ID); err != nil {
		return err
	}
	if err := s.references.DetachLayoutHistory(ctx, tx, layout.ID); err != nil {
		return err
	}
	documentID, parseErr := uuid.Parse(layout.ContentDocumentID)
	if parseErr != nil || documentID == uuid.Nil {
		return domainerr.Precondition("Email Layout Content Document is not initialized").WithCause(failurecause.ServerFault(failurecause.WithAttrs(parseErr, slog.String("layout_id", layout.ID))))
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, emailLayoutContentFence(s.references, layout.ID)); err != nil {
		return err
	}
	return tx.WithContext(ctx).Delete(layout).Error
}

// PreviewEmailLayoutQuery renders an Email Layout around sample content.
type PreviewEmailLayoutQuery struct {
	ID            string
	SampleContent *string
	Locale        *string
}

// PreviewEmailLayout renders the layout, localized when a locale is set,
// around the sample content or a default body.
func (s *Service) PreviewEmailLayout(ctx context.Context, query PreviewEmailLayoutQuery) (string, error) {
	can, canErr := policyv1.EmailLayout.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return "", err
	}
	layout, err := s.loadEmailLayout(ctx, query.ID)
	if err != nil {
		return "", err
	}
	sampleContent := "<p>This is sample email content that would appear inside the layout.</p>"
	if query.SampleContent != nil && *query.SampleContent != "" {
		sampleContent = *query.SampleContent
	}
	sampleData := s.sampleLayoutPreviewData(ctx, sampleContent, query.Locale)
	layoutHTML := layout.HTMLContent
	if query.Locale != nil && strings.TrimSpace(*query.Locale) != "" {
		localizedHTML, _, err := email.ResolveLocalizedEmailLayoutHTML(ctx, emailpostgres.Bind(s.db), layout.ID, *query.Locale)
		if err != nil {
			return "", err
		}
		layoutHTML = localizedHTML
	}
	renderedHTML, err := renderLayoutPreviewHTML(layoutHTML, sampleContent, sampleData)
	if err != nil {
		return "", domainerr.Precondition("email layout preview failed: " + err.Error())
	}
	return renderedHTML, nil
}

// PreviewEmailLayoutContentQuery validates and renders unsaved layout HTML
// for the editor's live preview.
type PreviewEmailLayoutContentQuery struct {
	HTMLContent   string
	SampleContent *string
	Locale        *string
}

// LayoutContentPreview is the rendered live preview, or the validation
// issues that kept it from rendering.
type LayoutContentPreview struct {
	Valid  bool
	HTML   string
	Errors []*managev1.EmailLayoutValidationError
}

// PreviewEmailLayoutContent validates the HTML and, when it is valid,
// renders it around the sample content or a default body.
func (s *Service) PreviewEmailLayoutContent(ctx context.Context, query PreviewEmailLayoutContentQuery) (LayoutContentPreview, error) {
	can, canErr := policyv1.EmailLayout.PreviewContent()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return LayoutContentPreview{}, err
	}
	htmlContent := email.NormalizeTemplatePlaceholders(query.HTMLContent)
	var validationErrors []*managev1.EmailLayoutValidationError
	for _, issue := range email.ValidateLayoutHTMLContent(htmlContent) {
		validationErrors = append(validationErrors, &managev1.EmailLayoutValidationError{
			Code:    issue.Code,
			Message: issue.Message,
		})
	}
	if len(validationErrors) > 0 {
		return LayoutContentPreview{Errors: validationErrors}, nil
	}
	sampleContent := "<h1>Welcome!</h1><p>This is sample email content that would appear inside the layout.</p><p>Thank you for subscribing.</p>"
	if query.SampleContent != nil && *query.SampleContent != "" {
		sampleContent = *query.SampleContent
	}
	sampleData := s.sampleLayoutPreviewData(ctx, sampleContent, query.Locale)
	renderedHTML, err := renderLayoutPreviewHTML(htmlContent, sampleContent, sampleData)
	if err != nil {
		return LayoutContentPreview{}, domainerr.InvalidField("html_content", err.Error())
	}
	return LayoutContentPreview{Valid: true, HTML: renderedHTML}, nil
}

// Helper functions

// renderLayoutPreviewHTML mirrors production layout rendering:
// 1) inject raw HTML into {{content}}, 2) render remaining escaped variables.
func renderLayoutPreviewHTML(layoutHTML, contentHTML string, data map[string]string) (string, error) {
	prepared, err := email.PrepareDraftLayoutHTML(layoutHTML)
	if err != nil {
		return "", err
	}
	return email.RenderEmailHTML(contentHTML, prepared, data)
}

func (s *Service) sampleLayoutPreviewData(
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

// emailLayoutFilterConfig defines filter fields for ListEmailLayouts.
var emailLayoutFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "key"},
		},
	},
}

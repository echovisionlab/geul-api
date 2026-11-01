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

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// layoutKeyRegex validates the format of layout keys
var layoutKeyRegex = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// lockEmailLayoutsForRelationMutation locks the existing layouts of
// layoutIDs, in ID order, and returns their IDs.
func lockEmailLayoutsForRelationMutation(
	ctx context.Context,
	store Store,
	layoutIDs ...string,
) ([]string, error) {
	unique := make(map[string]struct{}, len(layoutIDs))
	for _, layoutID := range layoutIDs {
		layoutID = strings.TrimSpace(layoutID)
		if layoutID != "" {
			unique[layoutID] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return []string{}, nil
	}
	normalized := make([]string, 0, len(unique))
	for layoutID := range unique {
		normalized = append(normalized, layoutID)
	}
	sort.Strings(normalized)
	return store.LockLayouts(ctx, normalized)
}

// loadCanonicalLayoutSource reads the layout's canonical source HTML.
func loadCanonicalLayoutSource(ctx context.Context, store Store, layoutID string) (string, error) {
	sourceLocale, found, err := store.LayoutSourceLocale(ctx, layoutID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", domainerr.Precondition("Email Layout source locale is not initialized")
	}
	state, err := store.CanonicalLayoutSource(ctx, layoutID, sourceLocale)
	if err != nil {
		return "", err
	}
	return derefString(state.ContentHTML), nil
}

func persistNewEmailLayoutCanonicalSource(
	ctx context.Context,
	store Store,
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
	return store.SaveLayoutSource(
		ctx,
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
func (s *Service) GetEmailLayout(ctx context.Context, query GetEmailLayoutQuery) (Layout, error) {
	can, canErr := policyv1.EmailLayout.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return Layout{}, err
	}
	var layout Layout
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if layout, err = loadEmailLayout(ctx, store, query.ID); err != nil {
			return err
		}
		layouts := []Layout{layout}
		if err := loadLayoutReferenceCounts(ctx, store, layouts); err != nil {
			return err
		}
		layout = layouts[0]
		return nil
	})
	return layout, err
}

// loadEmailLayout reads the layout with its canonical source HTML.
func loadEmailLayout(ctx context.Context, store Store, layoutID string) (Layout, error) {
	layout, err := store.FindLayout(ctx, layoutID)
	if errors.Is(err, ErrLayoutNotFound) {
		return Layout{}, domainerr.ResourceNotFound("email layout", layoutID)
	}
	if err != nil {
		return Layout{}, err
	}
	layout.HTMLContent, err = loadCanonicalLayoutSource(ctx, store, layout.ID)
	return layout, err
}

// ListEmailLayoutsQuery pages through Email Layouts for administrators.
type ListEmailLayoutsQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// EmailLayoutPage is one page of Email Layouts and the total of every
// match.
type EmailLayoutPage struct {
	Layouts []Layout
	Page    queryutil.Pagination
	Total   int64
}

// ListEmailLayouts returns one page of layouts with their source HTML and
// reference counts.
func (s *Service) ListEmailLayouts(ctx context.Context, query ListEmailLayoutsQuery) (EmailLayoutPage, error) {
	can, canErr := policyv1.EmailLayout.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return EmailLayoutPage{}, err
	}
	page := EmailLayoutPage{Page: queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)}
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		page.Layouts, page.Total, err = store.ListLayouts(ctx, ListQuery{Filters: query.Filters, Sorts: query.Sorts, Page: page.Page})
		if err != nil {
			return err
		}
		if err := loadLayoutReferenceCounts(ctx, store, page.Layouts); err != nil {
			return err
		}
		return overlayCanonicalEmailLayoutSourcesForList(ctx, store, page.Layouts)
	})
	if err != nil {
		return EmailLayoutPage{}, err
	}
	return page, nil
}

func overlayCanonicalEmailLayoutSourcesForList(ctx context.Context, store Store, layouts []Layout) error {
	if len(layouts) == 0 {
		return nil
	}

	ids := make([]string, 0, len(layouts))
	for i := range layouts {
		ids = append(ids, layouts[i].ID)
	}
	byID, err := store.CanonicalLayoutSources(ctx, ids)
	if err != nil {
		return err
	}
	for i := range layouts {
		html, ok := byID[layouts[i].ID]
		if !ok {
			// A listed layout without its canonical source row is corrupt data, not a missing
			// resource: fail closed as Internal instead of leaking NotFound for the whole list.
			return fmt.Errorf("email layout %s has no canonical source translation", layouts[i].ID)
		}
		layouts[i].HTMLContent = html
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
func newEmailLayout(command CreateEmailLayoutCommand) (Layout, error) {
	if err := validateLayoutKey(command.Key); err != nil {
		return Layout{}, domainerr.InvalidField("key", err.Error())
	}
	if err := validateLayoutName(command.Name); err != nil {
		return Layout{}, domainerr.InvalidField("name", err.Error())
	}
	if err := validateLayoutHTMLContent(command.HTMLContent); err != nil {
		return Layout{}, domainerr.InvalidField("html_content", err.Error())
	}
	htmlContent, err := email.CanonicalizeLayoutSourceMarkers(email.NormalizeTemplatePlaceholders(command.HTMLContent))
	if err != nil {
		return Layout{}, domainerr.InvalidField("html_content", err.Error())
	}
	sourceLocale, err := canonicalEmailLayoutRoomLocale(command.SourceLocale)
	if err != nil {
		return Layout{}, err
	}
	now := dbtime.Now()
	return Layout{
		SourceLocale: sourceLocale, Name: strings.TrimSpace(command.Name), Key: command.Key,
		HTMLContent: htmlContent, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// CreateEmailLayout creates the layout with its source locale document and
// grants its policy.
func (s *Service) CreateEmailLayout(ctx context.Context, command CreateEmailLayoutCommand) (Layout, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Layout, Store]{
		Access: mutation.FreshAdminCan(policyv1.EmailLayout.Create()),
		Change: func(ctx context.Context, store Store) (Layout, error) {
			layout, err := newEmailLayout(command)
			if err != nil {
				return Layout{}, err
			}
			document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
				Profile: emailLayoutContentProfile, SourceLocale: layout.SourceLocale,
			})
			if err != nil {
				return Layout{}, err
			}
			layout.ContentDocumentID = document.Document.ID.String()
			if err := store.InsertLayout(ctx, &layout); err != nil {
				if errors.Is(err, ErrLayoutKeyTaken) {
					return Layout{}, domainerr.Duplicate("email layout", "key", command.Key)
				}
				return Layout{}, err
			}
			return layout, persistNewEmailLayoutCanonicalSource(ctx, store, layout.ID, layout.SourceLocale, layout.HTMLContent, layout.CreatedAt)
		},
		Audit: func(layout Layout) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditEmailLayoutCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailLayoutCreatedAuditRecord(metadata, layout.ID)
			})
		},
		Relationships: func(layout Layout) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyLifecycle(policyv1.EmailLayout.TouchPolicy, policyv1.EmailLayout.DeletePolicy, layout.ID)
		},
	})
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
	layout Layout
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
func (s *Service) UpdateEmailLayout(ctx context.Context, command UpdateEmailLayoutCommand) (Layout, error) {
	access, err := adminAccess(policyv1.EmailLayout.Edit(command.ID))
	if err != nil {
		return Layout{}, err
	}
	updatedAt := dbtime.Now()
	var layout Layout
	update, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[emailLayoutUpdate, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			layout, err = lockMutableEmailLayout(ctx, store, command.ID)
			return err
		},
		Access: access,
		Change: func(ctx context.Context, store Store) (emailLayoutUpdate, error) {
			change, err := buildEmailLayoutChange(command)
			if err != nil {
				return emailLayoutUpdate{}, err
			}
			update := emailLayoutUpdate{fields: emailLayoutMetadataChangedFields(layout, &change)}
			if len(update.fields) > 0 {
				change.UpdatedAt = updatedAt
				if err := applyEmailLayoutChange(ctx, store, layout, command.Key, change); err != nil {
					return emailLayoutUpdate{}, err
				}
			}
			update.layout, err = loadEmailLayoutForResponse(ctx, store, layout.ID)
			return update, err
		},
		Audit: emailLayoutUpdate.audit,
	})
	if err != nil {
		return Layout{}, err
	}
	return update.layout, nil
}

func buildEmailLayoutChange(command UpdateEmailLayoutCommand) (LayoutChange, error) {
	var change LayoutChange
	if command.Name != nil {
		if err := validateLayoutName(*command.Name); err != nil {
			return LayoutChange{}, domainerr.InvalidField("name", err.Error())
		}
		name := strings.TrimSpace(*command.Name)
		change.Name = &name
	}
	if command.Key != nil {
		if err := validateLayoutKey(*command.Key); err != nil {
			return LayoutChange{}, domainerr.InvalidField("key", err.Error())
		}
		key := *command.Key
		change.Key = &key
	}
	return change, nil
}

// emailLayoutMetadataChangedFields names the fields change moves and drops
// the ones it does not.
func emailLayoutMetadataChangedFields(layout Layout, change *LayoutChange) []string {
	fields := make([]string, 0, 2)
	if change.Name != nil && *change.Name != layout.Name {
		fields = append(fields, "name")
	} else {
		change.Name = nil
	}
	if change.Key != nil && *change.Key != layout.Key {
		fields = append(fields, "key")
	} else {
		change.Key = nil
	}
	return fields
}

func lockMutableEmailLayout(ctx context.Context, store Store, layoutID string) (Layout, error) {
	layout, err := lockEmailLayout(ctx, store, layoutID)
	if err != nil {
		return Layout{}, err
	}
	return layout, store.RequireLayoutMutable(ctx, layout.ID)
}

func lockEmailLayout(ctx context.Context, store Store, layoutID string) (Layout, error) {
	layout, err := store.LockLayout(ctx, layoutID)
	if errors.Is(err, ErrLayoutNotFound) {
		return Layout{}, domainerr.ResourceNotFound("email layout", layoutID)
	}
	return layout, err
}

func applyEmailLayoutChange(ctx context.Context, store Store, layout Layout, requestedKey *string, change LayoutChange) error {
	if err := store.UpdateLayout(ctx, layout.ID, change); err != nil {
		if errors.Is(err, ErrLayoutKeyTaken) {
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

// loadEmailLayoutForResponse reads the stored layout with its source HTML
// and reference counts.
func loadEmailLayoutForResponse(ctx context.Context, store Store, layoutID string) (Layout, error) {
	layout, err := store.FindLayout(ctx, layoutID)
	if err != nil {
		return Layout{}, err
	}
	if layout.HTMLContent, err = loadCanonicalLayoutSource(ctx, store, layout.ID); err != nil {
		return Layout{}, err
	}
	layouts := []Layout{layout}
	if err := loadLayoutReferenceCounts(ctx, store, layouts); err != nil {
		return Layout{}, err
	}
	return layouts[0], nil
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
	var layout Layout
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			layout, err = lockEmailLayout(ctx, store, command.ID)
			return err
		},
		Access: access,
		Change: func(ctx context.Context, store Store) (string, error) {
			return layout.ID, deleteLockedEmailLayout(ctx, store, layout)
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

func deleteLockedEmailLayout(ctx context.Context, store Store, layout Layout) error {
	layouts := []Layout{layout}
	if err := loadLayoutReferenceCounts(ctx, store, layouts); err != nil {
		return err
	}
	layout = layouts[0]
	if layout.CampaignCount > 0 || layout.TemplateCount > 0 {
		return domainerr.Precondition("email layout must be unreferenced by campaigns and templates before deletion")
	}
	if err := store.RequireLayoutMutable(ctx, layout.ID); err != nil {
		return err
	}
	if err := store.DetachLayoutHistory(ctx, layout.ID); err != nil {
		return err
	}
	documentID, parseErr := uuid.Parse(layout.ContentDocumentID)
	if parseErr != nil || documentID == uuid.Nil {
		return domainerr.Precondition("Email Layout Content Document is not initialized").WithCause(failurecause.ServerFault(failurecause.WithAttrs(parseErr, slog.String("layout_id", layout.ID))))
	}
	if err := store.DeleteContentDocument(ctx, documentID, emailLayoutContentFence(store, layout.ID)); err != nil {
		return err
	}
	return store.DeleteLayout(ctx, layout.ID)
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
	var layout Layout
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		layout, err = loadEmailLayout(ctx, store, query.ID)
		return err
	}); err != nil {
		return "", err
	}
	sampleContent := "<p>This is sample email content that would appear inside the layout.</p>"
	if query.SampleContent != nil && *query.SampleContent != "" {
		sampleContent = *query.SampleContent
	}
	sampleData := s.sampleLayoutPreviewData(ctx, sampleContent, query.Locale)
	layoutHTML := layout.HTMLContent
	if query.Locale != nil && strings.TrimSpace(*query.Locale) != "" {
		if err := s.units.Read(ctx, func(store Store) error {
			var err error
			layoutHTML, err = store.LocalizedLayoutHTML(ctx, layout.ID, *query.Locale)
			return err
		}); err != nil {
			return "", err
		}
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
	Errors []LayoutValidationIssue
}

// LayoutValidationIssue is one reason layout HTML is invalid.
type LayoutValidationIssue struct {
	Code    string
	Message string
}

// PreviewEmailLayoutContent validates the HTML and, when it is valid,
// renders it around the sample content or a default body.
func (s *Service) PreviewEmailLayoutContent(ctx context.Context, query PreviewEmailLayoutContentQuery) (LayoutContentPreview, error) {
	can, canErr := policyv1.EmailLayout.PreviewContent()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return LayoutContentPreview{}, err
	}
	htmlContent := email.NormalizeTemplatePlaceholders(query.HTMLContent)
	var validationErrors []LayoutValidationIssue
	for _, issue := range email.ValidateLayoutHTMLContent(htmlContent) {
		validationErrors = append(validationErrors, LayoutValidationIssue{Code: issue.Code, Message: issue.Message})
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
	return s.renderData(ctx, requestedLocale, input)
}

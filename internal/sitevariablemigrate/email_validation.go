package sitevariablemigrate

import (
	"context"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

// Only content failures are skippable. Database and transaction failures still
// stop the run, rather than being mistaken for invalid authored content.
type emailValidationError struct {
	row string
	err error
}

func (e *emailValidationError) Error() string { return e.row + ": " + e.err.Error() }
func (e *emailValidationError) Unwrap() error { return e.err }

var emailPlaceholder = regexp.MustCompile(`{{\s*([^\s{}]+)\s*}}`)

func migrationEmailData(values sitevariable.Context, locale string, sources ...string) (map[string]string, error) {
	data := emaildelivery.BuildEmailRenderData(context.Background(), nil, os.Getenv("CDN_URL"), values.SiteURL, locale, nil)
	maps.Copy(data, values.Values())
	data["site_name"], data["site_origin"] = values.SiteTitle, values.SiteURL
	// Recipient/event-specific values are unavailable during a migration. As in
	// previews, supply inert sample values; reserved names always use settings.
	for _, source := range sources {
		for _, match := range emailPlaceholder.FindAllStringSubmatch(email.NormalizeTemplatePlaceholders(source), -1) {
			key := match[1]
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "site.") || strings.HasPrefix(lower, "contact.") {
				if !contentv1.IsSiteVariableKey(key) {
					return nil, fmt.Errorf("unknown site variable %q", key)
				}
				continue
			}
			if _, exists := data[key]; !exists {
				data[key] = "https://migration.invalid/sample"
			}
		}
	}
	return data, nil
}

// validateMigratedEmails reads the exact rows after all owner updates, inside
// the same transaction. This avoids locale fallback hiding a broken migrated
// row and ensures a subject-only change also validates the stored HTML.
func validateMigratedEmails(ctx context.Context, tx *gorm.DB, changes []change) error {
	seen := map[string]bool{}
	var values *sitevariable.Context
	for _, c := range changes {
		if c.spec.table != "email_template_translation" && c.spec.table != "email_layout_translation" {
			continue
		}
		rowKey := c.spec.table + " " + string(c.key)
		if seen[rowKey] {
			continue
		}
		seen[rowKey] = true
		if values == nil {
			loaded, err := sitevariable.LoadRuntime(ctx, tx)
			if err != nil {
				return err
			}
			values = &loaded
		}
		invalid := func(err error) error { return &emailValidationError{rowKey, err} }
		var subject, body, layout string
		if c.spec.table == "email_template_translation" {
			var row struct{ Subject, ContentHTML string }
			if err := tx.WithContext(ctx).Table(c.spec.table).Select("subject, content_html").
				Where("entity_id = ? AND locale = ?", c.owner, c.locale).Take(&row).Error; err != nil {
				return err
			}
			subject, body = row.Subject, row.ContentHTML
			var root struct{ LayoutID *string }
			if err := tx.WithContext(ctx).Table("email_template").Select("layout_id").Where("id = ?", c.owner).Take(&root).Error; err != nil {
				return err
			}
			if root.LayoutID != nil {
				var err error
				layout, _, err = email.ResolveLocalizedEmailLayoutHTML(ctx, tx, *root.LayoutID, c.locale)
				if err != nil {
					return invalid(err)
				}
			}
		} else {
			var row struct{ HTMLContent string }
			if err := tx.WithContext(ctx).Table(c.spec.table).Select("html_content").
				Where("entity_id = ? AND locale = ?", c.owner, c.locale).Take(&row).Error; err != nil {
				return err
			}
			// Validate the stored bytes too: resolving a sparse locale overlay can
			// omit obsolete units, which must not hide invalid migrated hrefs.
			_, sparse, err := email.LayoutMarkerPresence(row.HTMLContent)
			if err != nil {
				return invalid(err)
			}
			if !sparse {
				if err := email.ValidateLayoutHTMLContentError(row.HTMLContent); err != nil {
					return invalid(err)
				}
			}
			data, err := migrationEmailData(*values, c.locale, row.HTMLContent)
			if err != nil {
				return invalid(err)
			}
			if _, err := email.RenderHTMLVarsStrict(row.HTMLContent, data); err != nil {
				return invalid(err)
			}
			var source struct{ SourceLocale, HTMLContent string }
			if err := tx.WithContext(ctx).Table("email_layout AS root").Select("root.source_locale, source.html_content").
				Joins("JOIN email_layout_translation AS source ON source.entity_id = root.id AND source.locale = root.source_locale").
				Where("root.id = ?", c.owner).Take(&source).Error; err != nil {
				return err
			}
			canonical, _, err := email.MaterializeLayoutSourceLocale(source.HTMLContent)
			if err != nil {
				return invalid(err)
			}
			var target *string
			if c.locale != source.SourceLocale {
				target = &row.HTMLContent
			}
			layout, err = email.ResolveLayoutLocaleMarkup(*canonical, target)
			if err != nil {
				return invalid(err)
			}
			body = "<p>Migration validation</p>"
		}
		if layout != "" || c.spec.table == "email_layout_translation" {
			if err := email.ValidateLayoutHTMLContentError(layout); err != nil {
				return invalid(err)
			}
		}
		data, err := migrationEmailData(*values, c.locale, subject, body, layout)
		if err != nil {
			return invalid(err)
		}
		// Use the delivery snapshot renderer to compose and strictly render the
		// exact subject/body/wrapper, including href resolution and safety checks.
		snapshot := model.JSONFields{"subject": subject, "content_html": body}
		if layout != "" {
			snapshot["layout_source_locale"] = "en"
			snapshot["layout_translations"] = structured.Values{structured.Fields{"locale": "en", "html_content": layout}}
		}
		if _, err := email.RenderCampaignSnapshotForLocale(snapshot, "en", data); err != nil {
			return invalid(err)
		}
		if c.spec.table == "email_layout_translation" {
			_, displayedLocale, err := email.WrapWithLayoutForLocaleStrict(ctx, tx, c.owner, c.locale, body, data)
			if err != nil {
				return invalid(err)
			}
			if displayedLocale != c.locale {
				return invalid(fmt.Errorf("layout locale %s rendered fallback %s", c.locale, displayedLocale))
			}
		}
	}
	return nil
}

package email

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
	"golang.org/x/net/html"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BackfillLayoutMarkers upgrades one owner's source and all locale wrappers.
// Legacy positional matching is confined to this migration: it is accepted only
// when the complete non-translatable structure and ordered unit shapes agree.
// Runtime rendering continues to require durable identities.
func BackfillLayoutMarkers(sourceLocale string, locales map[string]string) (map[string]string, error) {
	source, ok := locales[sourceLocale]
	if !ok {
		return nil, fmt.Errorf("missing layout source locale %q", sourceLocale)
	}
	_, overlay, err := LayoutMarkerPresence(source)
	if err != nil {
		return nil, err
	}
	if overlay {
		materialized, _, err := MaterializeLayoutSourceLocale(source)
		if err != nil {
			return nil, err
		}
		source = *materialized
	}
	canonical, err := CanonicalizeLayoutSourceMarkers(source)
	if err != nil {
		return nil, err
	}
	units, err := ExtractLayoutContentUnits(canonical)
	if err != nil {
		return nil, err
	}
	result := map[string]string{sourceLocale: canonical}
	for locale, stored := range locales {
		if locale == sourceLocale {
			continue
		}
		marked, overlay, err := LayoutMarkerPresence(stored)
		if err != nil {
			return nil, fmt.Errorf("locale %s: %w", locale, err)
		}
		if marked || overlay {
			// Already durable rows must retain their IDs and sparse/empty semantics.
			if _, err := ExtractLayoutStoredLocaleValues(stored); err != nil {
				return nil, fmt.Errorf("locale %s: %w", locale, err)
			}
			result[locale] = stored
			continue
		}
		target, err := CanonicalizeLayoutSourceMarkers(stored)
		if err != nil {
			return nil, fmt.Errorf("locale %s: %w", locale, err)
		}
		targetUnits, err := ExtractLayoutContentUnits(target)
		if err != nil {
			return nil, fmt.Errorf("locale %s: %w", locale, err)
		}
		if len(units) != len(targetUnits) || layoutMigrationShape(source) != layoutMigrationShape(stored) {
			return nil, fmt.Errorf("locale %s: legacy layout structure differs from source; cannot safely align translated units", locale)
		}
		values := make(map[string]string, len(units))
		for i, unit := range units {
			other := targetUnits[i]
			if unit.Kind != other.Kind || unit.Element != other.Element || unit.Attribute != other.Attribute || unit.Context != other.Context {
				return nil, fmt.Errorf("locale %s: legacy layout unit %d differs from source", locale, i)
			}
			values[unit.Handle] = other.SourceValue
		}
		encoded, _, err := ApplyLayoutLocaleValues(canonical, values)
		if err != nil {
			return nil, fmt.Errorf("locale %s: %w", locale, err)
		}
		result[locale] = *encoded
	}
	return result, nil
}

// Compare every shared tag, non-localizable attribute, placeholder, and excluded
// text (CSS, etc.). Ignore only private markers and localizable text/attributes.
func layoutMigrationShape(content string) string {
	clean, err := StripLayoutPrivateMarkers(content)
	if err != nil {
		return "invalid:" + err.Error()
	}
	tokenizer := html.NewTokenizer(strings.NewReader(clean))
	var shape strings.Builder
	excludedDepth := 0
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return "invalid:" + tokenizer.Err().Error()
			}
			return shape.String()
		}
		token := tokenizer.Token()
		if kind == html.TextToken && excludedDepth == 0 && layoutTextIsTranslatable(token.Data) {
			// CSS and script contents must still compare exactly.
			token.Data = "#text"
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			for i := range token.Attr {
				if _, ok := layoutHTMLTranslatableAttributes[token.Attr[i].Key]; ok {
					token.Attr[i].Val = "#value"
				}
			}
		}
		if kind == html.TextToken && strings.TrimSpace(token.Data) == "" {
			continue
		}
		if kind == html.StartTagToken && translation.IsExcludedHTMLElement(token.DataAtom) {
			excludedDepth++
		}
		if kind == html.EndTagToken && translation.IsExcludedHTMLElement(token.DataAtom) && excludedDepth > 0 {
			excludedDepth--
		}
		fmt.Fprintf(&shape, "%d:%s\n", kind, token.String())
	}
}

// MigrateLayoutMarkers runs an administrative rewrite under the same owner
// lock used by Email Layout authoring. Source-locale changes invalidate plans.
// The caller supplies transaction-scoped backups and post-rewrite validation.
func MigrateLayoutMarkers(ctx context.Context, tx *gorm.DB, layoutID, expectedSource string, expectedLocales map[string]string, rewrite func() error) error {
	var root struct{ SourceLocale string }
	if err := tx.WithContext(ctx).Table("email_layout").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("source_locale").Where("id = ?", layoutID).Take(&root).Error; err != nil {
		return err
	}
	if root.SourceLocale != expectedSource {
		return fmt.Errorf("layout %s source locale changed during migration", layoutID)
	}
	// Lock all locale rows, including those whose bytes need no rewrite.
	var rows []struct {
		Locale      string
		HTMLContent *string
	}
	if err := tx.WithContext(ctx).Table("email_layout_translation").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("locale, html_content").Where("entity_id = ?", layoutID).Order("locale").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(expectedLocales) {
		return fmt.Errorf("layout %s locales changed during migration", layoutID)
	}
	for _, row := range rows {
		expected, ok := expectedLocales[row.Locale]
		if !ok || row.HTMLContent == nil || *row.HTMLContent != expected {
			return fmt.Errorf("layout %s locale %s changed during migration", layoutID, row.Locale)
		}
	}
	return rewrite()
}

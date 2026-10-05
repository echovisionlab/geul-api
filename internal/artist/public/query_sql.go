package public

import (
	"fmt"
	"strings"
)

func sourceColumnSQL(alias, entity, table, translationAlias, column string) string {
	if strings.TrimSpace(alias) == "" {
		alias = entity
	}
	return fmt.Sprintf("(SELECT %s.%s FROM %s AS %s JOIN %s AS source ON source.id = %s.entity_id AND source.source_locale = %s.locale WHERE %s.entity_id = %s.id LIMIT 1)", translationAlias, column, table, translationAlias, entity, translationAlias, translationAlias, translationAlias, alias)
}

func releaseSourceTitleSQL(alias string) string {
	return "COALESCE(" + sourceColumnSQL(alias, "release", "release_translation", "rt", "title") + ", '')"
}
func workSourceTitleSQL(alias string) string {
	return "COALESCE(" + sourceColumnSQL(alias, "work", "work_translation", "wt", "title") + ", '')"
}
func workSourceSummarySQL(alias string) string {
	return sourceColumnSQL(alias, "work", "work_translation", "wt", "summary")
}
func labelSourceTitleSQL(alias string) string {
	return "COALESCE(NULLIF(" + sourceColumnSQL(alias, "label", "label_translation", "lt", "title") + ", ''), '')"
}

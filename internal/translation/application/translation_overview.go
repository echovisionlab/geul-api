package application

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type translationOverviewStatsRow struct {
	TotalLocales    int64 `gorm:"column:total_locales"`
	EnabledLocales  int64 `gorm:"column:enabled_locales"`
	PublicLocales   int64 `gorm:"column:public_locales"`
	SourceEntities  int64 `gorm:"column:source_entities"`
	ActiveJobs      int64 `gorm:"column:active_jobs"`
	ExistingEntries int64 `gorm:"column:existing_entries"`
}

type translationLocaleHealthRow struct {
	Code                      string         `gorm:"column:code"`
	DisplayName               string         `gorm:"column:display_name"`
	Enabled                   bool           `gorm:"column:enabled"`
	IsPublic                  bool           `gorm:"column:is_public"`
	Dir                       string         `gorm:"column:dir"`
	MachineTranslationAllowed bool           `gorm:"column:machine_translation_allowed"`
	FontProfile               sql.NullString `gorm:"column:font_profile"`
	SortOrder                 int32          `gorm:"column:sort_order"`
	ExistingEntries           int64          `gorm:"column:existing_entries"`
	ActiveJobs                int64          `gorm:"column:active_jobs"`
	LastTargetUpdateAt        sql.NullTime   `gorm:"column:last_target_update_at"`
}

type translationEntityHealthRow struct {
	EntityType         string       `gorm:"column:entity_type"`
	SourceEntities     int64        `gorm:"column:source_entities"`
	ExistingEntries    int64        `gorm:"column:existing_entries"`
	ActiveJobs         int64        `gorm:"column:active_jobs"`
	LastSourceUpdateAt sql.NullTime `gorm:"column:last_source_update_at"`
}

func (s *TranslationService) getTranslationOverviewStats(
	ctx context.Context,
) (*managev1.TranslationOverviewStats, error) {
	query := fmt.Sprintf(`
WITH locale_totals AS (
	SELECT
		COUNT(*) AS total_locales,
		COUNT(*) FILTER (WHERE enabled) AS enabled_locales,
		COUNT(*) FILTER (WHERE is_public) AS public_locales
	FROM translation_locale
),
source_totals AS (
	SELECT
		COUNT(*) AS source_entities
	FROM (
		%s
	) AS translation_sources
),
entry_totals AS (
	SELECT COUNT(*) AS existing_entries
	FROM (
		%s
	) AS translation_entries
),
job_totals AS (
	SELECT COUNT(*) AS active_jobs
	FROM translation_job
	WHERE status IN ('%s', '%s')
)
SELECT
	locale_totals.total_locales,
	locale_totals.enabled_locales,
	locale_totals.public_locales,
	source_totals.source_entities,
	job_totals.active_jobs,
	entry_totals.existing_entries
FROM locale_totals
CROSS JOIN source_totals
CROSS JOIN entry_totals
CROSS JOIN job_totals
`,
		translationOverviewSourceUnionSQL(),
		translationOverviewEntryUnionSQL(),
		translationJobStatusQueued,
		translationJobStatusRunning,
	)

	var row translationOverviewStatsRow
	if err := s.db.WithContext(ctx).Raw(query).Scan(&row).Error; err != nil {
		return nil, errs.Internal(err)
	}

	stats := &managev1.TranslationOverviewStats{
		TotalLocales:    int32(row.TotalLocales),
		EnabledLocales:  int32(row.EnabledLocales),
		PublicLocales:   int32(row.PublicLocales),
		SourceEntities:  int32(row.SourceEntities),
		ActiveJobs:      int32(row.ActiveJobs),
		ExistingEntries: int32(row.ExistingEntries),
	}

	return stats, nil
}

func (s *TranslationService) listTranslationLocaleHealth(
	ctx context.Context,
) ([]*managev1.TranslationLocaleHealth, error) {
	query := fmt.Sprintf(`
WITH entry_counts AS (
	SELECT
		locale,
		COUNT(*) AS existing_entries,
		MAX(updated_at) AS last_target_update_at
	FROM (
		%s
	) AS translation_entries
	GROUP BY locale
),
job_counts AS (
	SELECT
		target_locale AS locale,
		COUNT(*) AS active_jobs
	FROM translation_job
	WHERE status IN ('%s', '%s')
	GROUP BY target_locale
)
SELECT
	l.code,
	l.display_name,
	l.enabled,
	l.is_public,
	l.dir,
	l.machine_translation_allowed,
	l.font_profile,
	l.sort_order,
	COALESCE(entry_counts.existing_entries, 0) AS existing_entries,
	COALESCE(job_counts.active_jobs, 0) AS active_jobs,
	entry_counts.last_target_update_at
FROM translation_locale l
LEFT JOIN entry_counts ON entry_counts.locale = l.code
LEFT JOIN job_counts ON job_counts.locale = l.code
ORDER BY l.sort_order ASC, l.code ASC
`,
		translationOverviewEntryUnionSQL(),
		translationJobStatusQueued,
		translationJobStatusRunning,
	)

	var rows []translationLocaleHealthRow
	if err := s.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}

	resp := make([]*managev1.TranslationLocaleHealth, 0, len(rows))
	for _, row := range rows {
		locale := localization.RuntimeLocale{
			Code:                      row.Code,
			DisplayName:               row.DisplayName,
			Enabled:                   row.Enabled,
			IsPublic:                  row.IsPublic,
			Dir:                       row.Dir,
			MachineTranslationAllowed: row.MachineTranslationAllowed,
			FontProfile:               nullableStringPointer(row.FontProfile),
			SortOrder:                 row.SortOrder,
		}

		item := &managev1.TranslationLocaleHealth{
			Locale:          toProtoTranslationLocale(locale),
			ExistingEntries: int32(row.ExistingEntries),
			ActiveJobs:      int32(row.ActiveJobs),
		}
		if row.LastTargetUpdateAt.Valid {
			item.LastTargetUpdateAt = timestamppb.New(row.LastTargetUpdateAt.Time)
		}
		resp = append(resp, item)
	}

	return resp, nil
}

func (s *TranslationService) listTranslationEntityHealth(
	ctx context.Context,
) ([]*managev1.TranslationEntityHealth, error) {
	query := fmt.Sprintf(`
	WITH entity_types(entity_type, sort_order) AS (
		%s
),
source_counts AS (
	SELECT
		entity_type,
		COUNT(*) AS source_entities,
		MAX(updated_at) AS last_source_update_at
	FROM (
		%s
	) AS translation_sources
	GROUP BY entity_type
),
entry_counts AS (
	SELECT
		entity_type,
		COUNT(*) AS existing_entries
	FROM (
		%s
	) AS translation_entries
	GROUP BY entity_type
),
job_counts AS (
	SELECT
		entity_type,
		COUNT(*) AS active_jobs
	FROM translation_job
	WHERE status IN ('%s', '%s')
	GROUP BY entity_type
)
SELECT
	entity_types.entity_type,
	COALESCE(source_counts.source_entities, 0) AS source_entities,
	COALESCE(entry_counts.existing_entries, 0) AS existing_entries,
	COALESCE(job_counts.active_jobs, 0) AS active_jobs,
	source_counts.last_source_update_at
FROM entity_types
LEFT JOIN source_counts ON source_counts.entity_type = entity_types.entity_type
LEFT JOIN entry_counts ON entry_counts.entity_type = entity_types.entity_type
LEFT JOIN job_counts ON job_counts.entity_type = entity_types.entity_type
ORDER BY entity_types.sort_order ASC
	`,
		translationOverviewEntityValuesSQL(),
		translationOverviewSourceUnionSQL(),
		translationOverviewEntryUnionSQL(),
		translationJobStatusQueued,
		translationJobStatusRunning,
	)

	var rows []translationEntityHealthRow
	if err := s.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}

	resp := make([]*managev1.TranslationEntityHealth, 0, len(rows))
	for _, row := range rows {
		definition, _ := translation.DefinitionForKind(row.EntityType)
		item := &managev1.TranslationEntityHealth{
			EntityType:      definition.Proto,
			SourceEntities:  int32(row.SourceEntities),
			ExistingEntries: int32(row.ExistingEntries),
			ActiveJobs:      int32(row.ActiveJobs),
		}
		if row.LastSourceUpdateAt.Valid {
			item.LastSourceUpdateAt = timestamppb.New(row.LastSourceUpdateAt.Time)
		}
		resp = append(resp, item)
	}

	return resp, nil
}

func translationOverviewEntryUnionSQL() string {
	definitions := translation.Definitions()
	queries := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		query := fmt.Sprintf(
			`SELECT '%s'::text AS entity_type, entry.entity_id::text AS entity_id, entry.locale, entry.updated_at `+
				`FROM %s AS entry JOIN %s AS root ON root.id = entry.entity_id `+
				`WHERE entry.locale <> root.source_locale`,
			definition.Kind,
			definition.EntryTable,
			definition.RootTable,
		)
		queries = append(queries, query)
	}
	return strings.Join(queries, "\nUNION ALL\n")
}

func translationOverviewSourceUnionSQL() string {
	definitions := translation.Definitions()
	queries := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		queries = append(queries, fmt.Sprintf(
			`SELECT '%s'::text AS entity_type, id::text AS entity_id, updated_at FROM %s`,
			definition.Kind,
			definition.RootTable,
		))
	}
	return strings.Join(queries, "\nUNION ALL\n")
}

func translationOverviewEntityValuesSQL() string {
	definitions := translation.Definitions()
	values := make([]string, 0, len(definitions))
	for index, definition := range definitions {
		values = append(values, fmt.Sprintf("('%s', %d)", definition.Kind, index+1))
	}
	return "VALUES\n\t\t" + strings.Join(values, ",\n\t\t")
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

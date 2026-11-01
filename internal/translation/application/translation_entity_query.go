package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Service) listEntityTranslations(
	ctx context.Context,
	entityType string,
	entityID string,
) ([]*managev1.TranslationEntry, error) {
	definition, err := translationTargetDefinition(entityType)
	if err != nil {
		return nil, err
	}
	table, idValue := definition.EntryTable, entityID
	selectSQL, err := s.domains.TranslationEntrySelectSQL(entityType, table)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.WithContext(ctx).Raw(
		fmt.Sprintf(`%s WHERE entity_id = ? ORDER BY locale ASC`, selectSQL),
		idValue,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]*managev1.TranslationEntry, 0)
	for rows.Next() {
		entry, err := s.scanTranslationEntryRow(ctx, rows, entityType, entityID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Service) getEntityTranslation(
	ctx context.Context,
	entityType string,
	entityID string,
	locale string,
) (*managev1.TranslationEntry, error) {
	definition, err := translationTargetDefinition(entityType)
	if err != nil {
		return nil, err
	}
	table, idValue := definition.EntryTable, entityID
	selectSQL, err := s.domains.TranslationEntrySelectSQL(entityType, table)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.WithContext(ctx).Raw(
		fmt.Sprintf(`%s WHERE entity_id = ? AND locale = ? LIMIT 1`, selectSQL),
		idValue,
		locale,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, domainerr.ResourceNotFound("translation_entry", entityID+":"+locale)
	}
	entry, err := s.scanTranslationEntryRow(ctx, rows, entityType, entityID)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *Service) resolveRequestedLocales(
	ctx context.Context,
	sourceLocale string,
	requested []string,
) ([]string, error) {
	locales, err := localization.NewCatalog(s.db).MachineTranslationTargets(ctx)
	if err != nil {
		return nil, err
	}

	if len(requested) == 0 {
		resolved := make([]string, 0, len(locales))
		for _, locale := range locales {
			if locale.Code == sourceLocale {
				continue
			}
			resolved = append(resolved, locale.Code)
		}
		return resolved, nil
	}

	allowed := make(map[string]struct{}, len(locales))
	for _, locale := range locales {
		allowed[locale.Code] = struct{}{}
	}
	resolved := make([]string, 0, len(requested))
	seen := make(map[string]struct{})
	for _, code := range requested {
		code = strings.TrimSpace(code)
		if code == "" || code == sourceLocale {
			continue
		}
		if _, ok := allowed[code]; !ok {
			return nil, domainerr.InvalidField("locales", fmt.Sprintf("unsupported locale %q", code))
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		resolved = append(resolved, code)
	}
	return resolved, nil
}

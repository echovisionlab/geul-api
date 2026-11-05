package application

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// ListEntityTranslationsQuery lists the stored locales of one entity.
type ListEntityTranslationsQuery struct {
	Target *Target
}

// EntityTranslations are the stored locale entries of one entity.
type EntityTranslations struct {
	SourceLocale string
	Entries      []Entry
}

// ListEntityTranslations lists the stored locale entries of an entity.
func (s *Service) ListEntityTranslations(ctx context.Context, query ListEntityTranslationsQuery) (EntityTranslations, error) {
	entityType, entityID, err := parseTranslationTarget(query.Target)
	if err != nil {
		return EntityTranslations{}, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return EntityTranslations{}, err
	}
	var translations EntityTranslations
	err = s.units.Read(ctx, func(store Store) error {
		authority, err := loadTranslationDocumentAuthority(ctx, store, entityType, entityID)
		if err != nil {
			return err
		}
		entries, err := store.Entries(ctx, entityType, entityID)
		if err != nil {
			return err
		}
		translations = EntityTranslations{SourceLocale: authority.SourceLocale, Entries: entries}
		return nil
	})
	if err != nil {
		return EntityTranslations{}, err
	}
	if translations.Entries == nil {
		translations.Entries = []Entry{}
	}
	return translations, nil
}

// GetEntityTranslationQuery reads one stored locale of an entity.
type GetEntityTranslationQuery struct {
	Target *Target
	Locale string
}

// GetEntityTranslation reads one stored locale entry of an entity.
func (s *Service) GetEntityTranslation(ctx context.Context, query GetEntityTranslationQuery) (Entry, error) {
	entityType, entityID, err := parseTranslationTarget(query.Target)
	if err != nil {
		return Entry{}, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return Entry{}, err
	}
	locale := strings.TrimSpace(query.Locale)
	var entry Entry
	err = s.units.Read(ctx, func(store Store) error {
		var err error
		entry, err = store.Entry(ctx, entityType, entityID, locale)
		return err
	})
	if errors.Is(err, ErrEntryNotFound) {
		return Entry{}, domainerr.ResourceNotFound("translation_entry", entityID+":"+locale)
	}
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// SetEntitySourceLocaleCommand makes SourceLocale the source of an entity's
// Content Document, guarded by the document revision the caller read.
type SetEntitySourceLocaleCommand struct {
	Target                   *Target
	SourceLocale             string
	ExpectedDocumentRevision string
}

// SourceLocaleChange is the outcome of a source-locale switch.
type SourceLocaleChange struct {
	DocumentRevision string
	Changed          bool
	ChangedLocales   []string
}

// SetEntitySourceLocale switches an entity's source locale.
func (s *Service) SetEntitySourceLocale(ctx context.Context, command SetEntitySourceLocaleCommand) (SourceLocaleChange, error) {
	entityType, entityID, err := parseTranslationTarget(command.Target)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	sourceLocale := strings.TrimSpace(command.SourceLocale)
	if err := s.validateRequestedSourceLocale(ctx, sourceLocale); err != nil {
		return SourceLocaleChange{}, err
	}
	expectedDocumentRevision, err := parseExpectedTranslationDocumentRevision(command.ExpectedDocumentRevision)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	requestingMemberID, err := requireAuthenticatedTranslationRequester(ctx)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	result, err := s.applySourceLocaleSwitch(ctx, entityType, entityID, sourceLocale, expectedDocumentRevision, requestingMemberID)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	return SourceLocaleChange{
		DocumentRevision: result.documentRevision,
		Changed:          !result.unchanged,
		ChangedLocales:   result.changedLocales,
	}, nil
}

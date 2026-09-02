package application

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

type sourceLocaleSwitchResult struct {
	unchanged        bool
	now              time.Time
	documentRevision string
	changedLocales   []string
}

type sourceLocaleSwitchState struct {
	entityType               string
	entityID                 string
	requestedLocale          string
	expectedDocumentRevision uuid.UUID
	contributorMemberID      string
	previousLocale           string
	contentBlocks            *contentblock.Store
	result                   sourceLocaleSwitchResult
}

type translationDocumentAuthority struct {
	SourceLocale     string    `gorm:"column:source_locale"`
	DocumentID       uuid.UUID `gorm:"column:content_document_id"`
	DocumentRevision uuid.UUID `gorm:"column:document_revision"`
}

func parseExpectedTranslationDocumentRevision(
	entityType string,
	value string,
) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	revision, err := uuid.Parse(normalized)
	if err != nil || revision == uuid.Nil || revision.String() != normalized {
		return uuid.Nil, errs.InvalidArgument(
			"expected_document_revision",
			"a canonical Content Document revision UUID is required",
		)
	}
	return revision, nil
}

func validateRequestedSourceLocale(ctx context.Context, db *gorm.DB, locale string) error {
	if strings.TrimSpace(locale) == "" {
		return errs.InvalidArgument("source_locale", "source locale is required")
	}
	if _, err := localization.NewCatalog(db).Find(ctx, locale); err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.InvalidArgument("source_locale", "unsupported locale")
		}
		return errs.Internal(err)
	}
	return nil
}

func (s *TranslationService) applySourceLocaleSwitch(
	ctx context.Context,
	entityType string,
	entityID string,
	sourceLocale string,
	expectedDocumentRevision uuid.UUID,
	contributorMemberID string,
) (sourceLocaleSwitchResult, error) {
	state := sourceLocaleSwitchState{
		entityType:               entityType,
		entityID:                 entityID,
		requestedLocale:          sourceLocale,
		expectedDocumentRevision: expectedDocumentRevision,
		contributorMemberID:      contributorMemberID,
		contentBlocks:            s.contentBlocks,
		result: sourceLocaleSwitchResult{
			now: s.now().UTC(),
		},
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockSourceLocaleSwitch(ctx, tx, &state); err != nil {
			return err
		}
		authority, err := loadTranslationDocumentAuthority(ctx, tx, state.entityType, state.entityID)
		if err != nil {
			return err
		}
		state.previousLocale = authority.SourceLocale
		if err := s.domains.RequireDocumentContributors(ctx, tx, []string{state.contributorMemberID}); err != nil {
			return err
		}
		return s.performSourceLocaleSwitch(ctx, tx, &state, authority)
	})
	return state.result, err
}

func loadTranslationDocumentAuthority(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (translationDocumentAuthority, error) {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return translationDocumentAuthority{}, errs.InvalidArgument("target.entity_type", "unsupported translation entity type")
	}
	var authority translationDocumentAuthority
	result := db.WithContext(ctx).Raw(
		"SELECT root.source_locale, root.content_document_id, document.revision AS document_revision FROM "+definition.RootTable+" AS root "+
			"JOIN content_document AS document ON document.id = root.content_document_id "+
			"WHERE root.id = ?",
		entityID,
	).Scan(&authority)
	if result.Error != nil {
		return translationDocumentAuthority{}, errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return translationDocumentAuthority{}, errs.NotFound(entityType, entityID)
	}
	if strings.TrimSpace(authority.SourceLocale) == "" || authority.DocumentID == uuid.Nil ||
		authority.DocumentRevision == uuid.Nil {
		return translationDocumentAuthority{}, errs.FailedPrecondition("Content Document authority is not initialized")
	}
	return authority, nil
}

func (s *TranslationService) lockSourceLocaleSwitch(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
) error {
	if s.domains == nil {
		return errs.InternalMsg("translation domain registry is required")
	}
	if err := s.domains.LockRoot(ctx, tx, state.entityType, state.entityID); err != nil {
		return err
	}
	if err := s.domains.RequireTranslationSourceMutable(
		ctx, tx, state.entityType, state.entityID,
	); err != nil {
		return err
	}
	if err := requireEditableTranslationDomain(ctx, tx, s.domains, state.entityType, state.entityID); err != nil {
		return err
	}
	// Source-locale reassignment is one authoritative operation. The owning
	// domain selects Edit or EditArchived from the already locked lifecycle and
	// performs exactly one current-principal ReBAC decision here.
	if err := s.domains.RequireSourceLocaleEdit(
		ctx, tx, s.spiceDB, state.entityType, state.entityID,
	); err != nil {
		return err
	}
	return nil
}

func (s *TranslationService) performSourceLocaleSwitch(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
	authority translationDocumentAuthority,
) error {
	if state.contentBlocks == nil {
		return errs.InternalMsg("Content Document store is required")
	}
	definition, ok := translation.DefinitionForKind(state.entityType)
	if !ok {
		return errs.InvalidArgument("target.entity_type", "unsupported translation entity type")
	}
	changedLocales, err := listSourceSwitchLocales(
		ctx, tx, definition.EntryTable, state.entityID, authority.DocumentID,
		authority.SourceLocale, state.requestedLocale,
	)
	if err != nil {
		return err
	}
	contributor, parseErr := uuid.Parse(state.contributorMemberID)
	if parseErr != nil || contributor == uuid.Nil || contributor.String() != state.contributorMemberID {
		return errs.InternalMsg("authenticated translation requester Member is invalid")
	}
	switched, err := state.contentBlocks.SwitchSourceLocale(
		ctx,
		tx,
		contentblock.SourceLocaleSwitchInput{
			DocumentID:           authority.DocumentID,
			ExpectedRevision:     state.expectedDocumentRevision,
			RequestedLocale:      state.requestedLocale,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{
				SourceLocale: authority.SourceLocale,
			}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			if authority.SourceLocale == state.requestedLocale {
				return contentblock.MetadataEffect{}, nil
			}
			if err := s.domains.PrepareSourceLocale(
				ctx,
				tx,
				state.entityType,
				state.entityID,
				authority.SourceLocale,
				state.requestedLocale,
				state.result.now,
			); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			updated := tx.WithContext(ctx).Exec(
				"UPDATE "+definition.RootTable+" SET source_locale = ?, updated_at = ? WHERE id = ? AND source_locale = ? AND content_document_id = ?",
				state.requestedLocale,
				state.result.now,
				state.entityID,
				authority.SourceLocale,
				authority.DocumentID,
			)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, translation.ErrSourceNoLongerCurrent
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true,
				SourceLocale: state.requestedLocale,
			}, nil
		},
	)
	if err != nil {
		return err
	}
	state.result.documentRevision = switched.DocumentRevision.String()
	state.result.unchanged = !switched.Changed
	if state.result.unchanged {
		return nil
	}
	state.result.changedLocales = changedLocales
	if err := s.requestSourceLocaleSwitchOg(ctx, tx, state); err != nil {
		return err
	}
	return s.appendSourceLocaleSwitchAudit(ctx, tx, state)
}

func listSourceSwitchLocales(
	ctx context.Context,
	tx *gorm.DB,
	entryTable string,
	entityID string,
	documentID uuid.UUID,
	previousLocale string,
	nextLocale string,
) ([]string, error) {
	locales := []string{previousLocale, nextLocale}
	var entryLocales []string
	if err := tx.WithContext(ctx).Table(entryTable).
		Where("entity_id = ?", entityID).Pluck("locale", &entryLocales).Error; err != nil {
		return nil, errs.Internal(err)
	}
	locales = append(locales, entryLocales...)
	var blockLocales []string
	if err := tx.WithContext(ctx).Raw(
		`SELECT DISTINCT locale FROM content_block_locale
		 WHERE block_id IN (SELECT id FROM content_block WHERE document_id = ?)`,
		documentID,
	).Scan(&blockLocales).Error; err != nil {
		return nil, errs.Internal(err)
	}
	locales = append(locales, blockLocales...)
	return uniqueSortedStrings(locales), nil
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *TranslationService) requestSourceLocaleSwitchOg(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
) error {
	reason := state.entityType + "_source_locale_changed"
	if s.domains == nil {
		return errs.InternalMsg("translation domain registry is required")
	}
	_, err := s.domains.RequestLocaleOG(
		ctx, tx, s.ogPlanner, s.ogRefresher,
		state.entityType, state.entityID, state.requestedLocale, reason,
	)
	return err
}

func (s *TranslationService) appendSourceLocaleSwitchAudit(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
) error {
	if s.domains == nil {
		return errs.InternalMsg("translation domain registry is required")
	}
	return s.domains.AppendSourceLocaleAudit(
		ctx, tx, state.entityType, state.entityID, state.previousLocale, state.requestedLocale,
	)
}

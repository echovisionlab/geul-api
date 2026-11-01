package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

type sourceLocaleSwitchResult struct {
	unchanged        bool
	now              time.Time
	documentRevision string
	changedLocales   []string
	audit            SourceLocaleAudit
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
	value string,
) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	revision, err := uuid.Parse(normalized)
	if err != nil || revision == uuid.Nil || revision.String() != normalized {
		return uuid.Nil, domainerr.InvalidField(
			"expected_document_revision",
			"a canonical Content Document revision UUID is required",
		)
	}
	return revision, nil
}

func validateRequestedSourceLocale(ctx context.Context, db *gorm.DB, locale string) error {
	if strings.TrimSpace(locale) == "" {
		return domainerr.InvalidField("source_locale", "source locale is required")
	}
	if _, err := localization.NewCatalog(db).Find(ctx, locale); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.InvalidField("source_locale", "unsupported locale")
		}
		return err
	}
	return nil
}

func (s *Service) applySourceLocaleSwitch(
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
	return mutation.Run(ctx, s.mutations, mutation.Spec[sourceLocaleSwitchResult]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return s.lockSourceLocaleSwitch(ctx, tx, &state)
		},
		// Source-locale reassignment is one authoritative operation. The owning
		// domain selects Edit or EditArchived from the already locked lifecycle
		// and performs exactly one current-principal ReBAC decision here.
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return s.domains.RequireSourceLocaleEdit(ctx, tx, s.spiceDB, state.entityType, state.entityID)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (sourceLocaleSwitchResult, error) {
			authority, err := loadTranslationDocumentAuthority(ctx, tx, state.entityType, state.entityID)
			if err != nil {
				return sourceLocaleSwitchResult{}, err
			}
			state.previousLocale = authority.SourceLocale
			if err := s.domains.RequireDocumentContributors(ctx, tx, []string{state.contributorMemberID}); err != nil {
				return sourceLocaleSwitchResult{}, err
			}
			err = s.performSourceLocaleSwitch(ctx, tx, &state, authority)
			return state.result, err
		},
		Audit: func(result sourceLocaleSwitchResult) mutation.Audit {
			if result.unchanged {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(result.audit.Action, result.audit.Build)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, result sourceLocaleSwitchResult) error {
			if result.unchanged {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildTypedSourceLocaleUpdatedEvent(
				state.entityType, state.entityID, result.documentRevision,
			))
		},
	})
}

func loadTranslationDocumentAuthority(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (translationDocumentAuthority, error) {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return translationDocumentAuthority{}, domainerr.InvalidField("target.entity_type", "unsupported translation entity type")
	}
	var authority translationDocumentAuthority
	result := db.WithContext(ctx).Raw(
		"SELECT source_locale, content_document_id FROM "+definition.RootTable+" WHERE id = ?",
		entityID,
	).Scan(&authority)
	if result.Error != nil {
		return translationDocumentAuthority{}, result.Error
	}
	// A root without its Content Document is as absent as a missing root.
	if result.RowsAffected != 1 || authority.DocumentID == uuid.Nil {
		return translationDocumentAuthority{}, domainerr.ResourceNotFound(entityType, entityID)
	}
	revision, err := contentblock.CurrentRevision(ctx, db, authority.DocumentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return translationDocumentAuthority{}, domainerr.ResourceNotFound(entityType, entityID)
	}
	if err != nil {
		return translationDocumentAuthority{}, err
	}
	authority.DocumentRevision = revision
	if strings.TrimSpace(authority.SourceLocale) == "" || authority.DocumentID == uuid.Nil ||
		authority.DocumentRevision == uuid.Nil {
		return translationDocumentAuthority{}, domainerr.Precondition("Content Document authority is not initialized")
	}
	return authority, nil
}

func (s *Service) lockSourceLocaleSwitch(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
) error {
	if err := s.domains.LockRoot(ctx, tx, state.entityType, state.entityID); err != nil {
		return err
	}
	if err := s.domains.RequireTranslationSourceMutable(
		ctx, tx, state.entityType, state.entityID,
	); err != nil {
		return err
	}
	return s.domains.RequireEditable(ctx, tx, state.entityType, state.entityID)
}

func (s *Service) performSourceLocaleSwitch(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
	authority translationDocumentAuthority,
) error {
	definition, ok := translation.DefinitionForKind(state.entityType)
	if !ok {
		return domainerr.InvalidField("target.entity_type", "unsupported translation entity type")
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
		return errors.New("authenticated translation requester Member is invalid")
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
				return contentblock.MetadataEffect{}, updated.Error
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
	state.result.audit, err = s.domains.SourceLocaleAudit(
		ctx, tx, state.entityType, state.entityID, state.previousLocale, state.requestedLocale,
	)
	return err
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
		return nil, err
	}
	locales = append(locales, entryLocales...)
	blockLocales, err := contentblock.DocumentLocales(ctx, tx, documentID)
	if err != nil {
		return nil, err
	}
	locales = sliceutil.UniqueNonZero(append(locales, blockLocales...))
	slices.Sort(locales)
	return locales, nil
}

func (s *Service) requestSourceLocaleSwitchOg(
	ctx context.Context,
	tx *gorm.DB,
	state *sourceLocaleSwitchState,
) error {
	reason := state.entityType + "_source_locale_changed"
	_, err := s.domains.RequestLocaleOG(
		ctx, tx, s.ogPlanner, s.ogRefresher,
		state.entityType, state.entityID, state.requestedLocale, reason,
	)
	return err
}

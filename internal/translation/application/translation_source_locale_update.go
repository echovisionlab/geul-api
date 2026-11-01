package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	result                   sourceLocaleSwitchResult
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

func (s *Service) validateRequestedSourceLocale(ctx context.Context, locale string) error {
	if strings.TrimSpace(locale) == "" {
		return domainerr.InvalidField("source_locale", "source locale is required")
	}
	return s.units.Read(ctx, func(store Store) error {
		return requireKnownLocale(ctx, store, "source_locale", locale)
	})
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
		result: sourceLocaleSwitchResult{
			now: s.now().UTC(),
		},
	}
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[sourceLocaleSwitchResult, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return lockSourceLocaleSwitch(ctx, store, &state)
		},
		// Source-locale reassignment is one authoritative operation. The owning
		// domain selects Edit or EditArchived from the already locked lifecycle
		// and performs exactly one current-principal ReBAC decision here.
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return store.Domains().RequireSourceLocaleEdit(ctx, state.entityType, state.entityID)
		}),
		Change: func(ctx context.Context, store Store) (sourceLocaleSwitchResult, error) {
			authority, err := loadTranslationDocumentAuthority(ctx, store, state.entityType, state.entityID)
			if err != nil {
				return sourceLocaleSwitchResult{}, err
			}
			state.previousLocale = authority.SourceLocale
			if err := store.Domains().RequireDocumentContributors(ctx, []string{state.contributorMemberID}); err != nil {
				return sourceLocaleSwitchResult{}, err
			}
			err = performSourceLocaleSwitch(ctx, store, &state, authority)
			return state.result, err
		},
		Audit: func(result sourceLocaleSwitchResult) mutation.Audit {
			if result.unchanged {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(result.audit.Action, result.audit.Build)
		},
		Outbox: func(ctx context.Context, store Store, result sourceLocaleSwitchResult) error {
			if result.unchanged {
				return nil
			}
			return store.PublishSourceLocaleChanged(ctx, state.entityType, state.entityID, result.documentRevision)
		},
	})
}

// loadTranslationDocumentAuthority reads the root's source locale and
// Content Document revision; a root without its document is as absent as a
// missing root.
func loadTranslationDocumentAuthority(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
) (DocumentAuthority, error) {
	if _, ok := translation.DefinitionForKind(entityType); !ok {
		return DocumentAuthority{}, domainerr.InvalidField("target.entity_type", "unsupported translation entity type")
	}
	authority, err := store.DocumentAuthority(ctx, entityType, entityID)
	switch {
	case errors.Is(err, ErrEntityNotFound):
		return DocumentAuthority{}, domainerr.ResourceNotFound(entityType, entityID)
	case err != nil:
		return DocumentAuthority{}, err
	}
	if strings.TrimSpace(authority.SourceLocale) == "" || authority.DocumentID == uuid.Nil ||
		authority.DocumentRevision == uuid.Nil {
		return DocumentAuthority{}, domainerr.Precondition("Content Document authority is not initialized")
	}
	return authority, nil
}

func lockSourceLocaleSwitch(
	ctx context.Context,
	store Store,
	state *sourceLocaleSwitchState,
) error {
	domains := store.Domains()
	if err := domains.LockRoot(ctx, state.entityType, state.entityID); err != nil {
		return err
	}
	if err := domains.RequireSourceMutable(ctx, state.entityType, state.entityID); err != nil {
		return err
	}
	return domains.RequireEditable(ctx, state.entityType, state.entityID)
}

func performSourceLocaleSwitch(
	ctx context.Context,
	store Store,
	state *sourceLocaleSwitchState,
	authority DocumentAuthority,
) error {
	if _, ok := translation.DefinitionForKind(state.entityType); !ok {
		return domainerr.InvalidField("target.entity_type", "unsupported translation entity type")
	}
	changedLocales, err := store.SourceSwitchLocales(
		ctx, state.entityType, state.entityID, authority.DocumentID,
		authority.SourceLocale, state.requestedLocale,
	)
	if err != nil {
		return err
	}
	contributor, parseErr := uuid.Parse(state.contributorMemberID)
	if parseErr != nil || contributor == uuid.Nil || contributor.String() != state.contributorMemberID {
		return errors.New("authenticated translation requester Member is invalid")
	}
	switched, err := store.SwitchSourceLocale(ctx, SourceLocaleSwitch{
		EntityType: state.entityType, EntityID: state.entityID, Authority: authority,
		Requested: state.requestedLocale, ExpectedRevision: state.expectedDocumentRevision,
		Contributor: contributor, Now: state.result.now,
	})
	if err != nil {
		return err
	}
	state.result.documentRevision = switched.DocumentRevision.String()
	state.result.unchanged = !switched.Changed
	if state.result.unchanged {
		return nil
	}
	state.result.changedLocales = changedLocales
	reason := state.entityType + "_source_locale_changed"
	if _, err := store.Domains().RequestLocaleOG(ctx, state.entityType, state.entityID, state.requestedLocale, reason); err != nil {
		return err
	}
	state.result.audit, err = store.Domains().SourceLocaleAudit(
		ctx, state.entityType, state.entityID, state.previousLocale, state.requestedLocale,
	)
	return err
}

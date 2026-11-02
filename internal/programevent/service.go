package programevent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the SpiceDB port of the Program Event use cases: the fresh
// checks and relationship writes of the mutation pipeline and the check of
// a collaboration session's own principal. *auth.SpiceDBClient implements
// it.
type Authorizer interface {
	mutation.Authorizer
	ActorChecker
}

// ActorChecker checks an action of an explicit actor.
type ActorChecker interface {
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

// CreditMemberSummaries projects Member-owned identity details for Program
// Event credits.
type CreditMemberSummaries interface {
	LoadCreditMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error)
}

// Deps are the collaborators of the Program Event, series, type,
// collaboration and AI document use cases; all are required.
type Deps struct {
	Units         mutation.UnitOfWork[Store] // programeventpostgres.NewUnits
	SpiceDB       Authorizer
	Fence         identitystate.Fence
	Audit         domainaudit.Appender
	CreditMembers CreditMemberSummaries
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"CreditMembers": d.CreditMembers == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("program event dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Program Event use cases of administrators, of the
// collaboration service and of the AI document adapter. Connect and MCP
// adapters translate their requests into its commands and queries.
type Service struct {
	units         mutation.UnitOfWork[Store]
	spiceDB       Authorizer
	mutations     *mutation.StoreRunner[Store]
	creditMembers CreditMemberSummaries
}

// NewService validates deps and returns the Program Event use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations, creditMembers: deps.CreditMembers,
	}, nil
}

// RequireExists verifies the Program Event root at cross-domain attachment
// boundaries.
func RequireExists(ctx context.Context, store Store, eventID string) error {
	if err := store.RequireEvent(ctx, eventID); err != nil {
		if errors.Is(err, ErrEventNotFound) {
			return domainerr.ResourceNotFound("program event", eventID)
		}
		return err
	}
	return nil
}

// resolveInitialSourceLocale is the source locale of a new document: the
// Member's preferred locale, then acceptLanguage, then the site default.
// Each lookup reads on its own, so one that fails leaves the next intact.
func (s *Service) resolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated {
		var preferredLocale *string
		err := s.units.Read(ctx, func(store Store) error {
			var err error
			preferredLocale, err = store.PreferredLocale(ctx, user.MemberID.String())
			return err
		})
		if err == nil && preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Program Event source locale from the request", "domain", "programevent", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	var settings translation.RuntimeSettings
	if err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		settings, err = store.TranslationSettings(ctx)
		return err
	}); err == nil {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	} else {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the default Program Event source locale", "domain", "programevent", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
	}
	return translation.DefaultLocale
}

// lockTranslationRoot locks the event a translation step writes; a missing
// event answers not found in the translation entity's terms.
func lockTranslationRoot(ctx context.Context, store Store, eventID string) error {
	if err := store.LockEventID(ctx, eventID); err != nil {
		if errors.Is(err, ErrEventNotFound) {
			return domainerr.ResourceNotFound(EntityType, eventID)
		}
		return err
	}
	return nil
}

func requireDocumentContributors(ctx context.Context, store Store, contributors []string) error {
	if len(contributors) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	for index, contributor := range contributors {
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires canonical Member UUIDs")
		}
		if index > 0 && contributors[index-1] >= contributor {
			return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
	}
	locked, err := store.LockMembers(ctx, contributors)
	if err != nil {
		return fmt.Errorf("lock Program Event contributor Members: %w", err)
	}
	if locked != len(contributors) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

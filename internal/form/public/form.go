// Package public owns the public Form access and submission boundary.
package public

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Deps are the collaborators of the public Form use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	Units    mutation.UnitOfWork[Store] // formpublicpostgres.NewUnits
	SpiceDB  mutation.Authorizer
	Fence    identitystate.Fence
	Audit    domainaudit.Appender
	Password *crypto.PasswordHasher
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Password": d.Password == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public form dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Form use cases: access checks, password checks,
// submissions and shared dashboards.
type Service struct {
	units     mutation.UnitOfWork[Store]
	password  *crypto.PasswordHasher
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the public Form use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, password: deps.Password, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}

type formShareTokenState struct {
	valid bool
}

// CheckFormAccessQuery asks whether a visitor may open a Form, or with
// Dashboard its shared dashboard. Embed is an embedded Form rather than its
// own URL.
type CheckFormAccessQuery struct {
	Slug           string
	Dashboard      bool
	Embed          bool
	ShareToken     *string
	SharePassword  *string
	Password       *string
	AcceptLanguage string
}

// Access is whether a visitor may open a Form and why. Form is the Form
// when it is accessible, its metadata when the denial may show it, and nil
// otherwise.
type Access struct {
	Accessible bool
	Reason     AccessReason
	Form       *View
}

// CheckFormAccess evaluates the unified form access policy and returns
// structured reasons.
func (s *Service) CheckFormAccess(ctx context.Context, query CheckFormAccessQuery) (Access, error) {
	found, err := s.findFormBySlugOrID(ctx, query.Slug)
	if err != nil {
		if mutation.IsNotFound(err) {
			return Access{Reason: ReasonFormNotFound}, nil
		}
		return Access{}, err
	}

	shareType := ShareLinkForm
	accessContext := ContextURL
	if query.Dashboard {
		shareType = ShareLinkFormDashboard
	} else if query.Embed {
		accessContext = ContextEmbed
	}
	shareTokenState := s.validateShareToken(ctx, &found, query.ShareToken, query.SharePassword, shareType)
	if query.Dashboard && !shareTokenState.valid {
		return Access{Reason: ReasonFormNotFound}, nil
	}

	reason, err := s.evaluateFormAccess(ctx, s.reads(), &found, formAccessOptions{
		context:                  accessContext,
		hasValidPreviewToken:     shareTokenState.valid,
		bypassAuth:               shareTokenState.valid,
		bypassRoles:              shareTokenState.valid,
		enforcePassword:          !shareTokenState.valid,
		password:                 query.Password,
		checkSubmissionLimit:     !query.Dashboard,
		checkDuplicateSubmission: !query.Dashboard,
	})
	if err != nil {
		return Access{}, err
	}

	access := Access{Accessible: reason == ReasonAllowed, Reason: reason}
	if access.Accessible {
		view, err := s.buildFormView(ctx, &found, query.AcceptLanguage, false)
		if err != nil {
			return Access{}, err
		}
		access.Form = &view
	} else if !query.Dashboard && formAccessDenialShowsMetadata(reason) {
		// Expose minimal metadata for denied-but-publicly-addressable forms
		// (e.g. password/auth/role-gated), but hide private/draft existence.
		view, err := s.buildFormView(ctx, &found, query.AcceptLanguage, true)
		if err != nil {
			return Access{}, err
		}
		access.Form = &view
	}
	return access, nil
}

func formAccessDenialShowsMetadata(reason AccessReason) bool {
	switch reason {
	case ReasonFormNotFound, ReasonFormNotPublished, ReasonNotPublic:
		return false
	default:
		return true
	}
}

// read runs read in one read snapshot of the public Store.
func (s *Service) read(ctx context.Context, read func(Store) error) error {
	return s.units.Read(ctx, read)
}

// Package public owns the public Form access and submission boundary.
package public

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Members reads the Member rows public Forms need; Member owns them.
type Members interface {
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
}

// ShareLinks reads the share links that open a Form or its dashboard; Share
// Link owns them.
type ShareLinks interface {
	// FindShareLink returns the share link of token, or
	// gorm.ErrRecordNotFound.
	FindShareLink(ctx context.Context, db *gorm.DB, token string) (*model.ShareLink, error)
}

// Deps are the collaborators of the public Form use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB         *gorm.DB
	SpiceDB    *auth.SpiceDBClient
	Password   *crypto.PasswordHasher
	Assets     formdomain.PublicAssets
	Members    Members
	ShareLinks ShareLinks
	Audit      domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Password": d.Password == nil, "Assets": d.Assets == nil,
		"Members": d.Members == nil, "ShareLinks": d.ShareLinks == nil, "Audit": d.Audit == nil,
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
	db         *gorm.DB
	password   *crypto.PasswordHasher
	spiceDB    *auth.SpiceDBClient
	assets     formdomain.PublicAssets
	members    Members
	shareLinks ShareLinks
	mutations  *mutation.Runner
}

// NewService validates deps and returns the public Form use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, password: deps.Password, spiceDB: deps.SpiceDB, assets: deps.Assets,
		members: deps.Members, shareLinks: deps.ShareLinks, mutations: mutations,
	}, nil
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

// CheckFormAccess evaluates the unified form access policy and returns
// structured reasons.
func (s *Service) CheckFormAccess(ctx context.Context, query CheckFormAccessQuery) (*openv1.CheckFormAccessResponse, error) {
	form, err := s.findFormBySlugOrID(ctx, query.Slug)
	if err != nil {
		if mutation.IsNotFound(err) {
			return &openv1.CheckFormAccessResponse{
				Accessible: false,
				Reason:     openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
			}, nil
		}
		return nil, err
	}

	shareType := managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM
	accessContext := openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL
	if query.Dashboard {
		shareType = managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD
	} else if query.Embed {
		accessContext = openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED
	}
	shareTokenState := s.validateShareToken(ctx, form, query.ShareToken, query.SharePassword, shareType)
	if query.Dashboard && !shareTokenState.valid {
		return &openv1.CheckFormAccessResponse{
			Accessible: false,
			Reason:     openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
		}, nil
	}

	reason, err := s.evaluateFormAccess(ctx, form, formAccessOptions{
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
		return nil, err
	}

	resp := &openv1.CheckFormAccessResponse{
		Accessible: reason == openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED,
		Reason:     reason,
	}
	if resp.Accessible {
		if resp.Form, err = s.buildProtoForm(ctx, form, query.AcceptLanguage); err != nil {
			return nil, err
		}
	} else if !query.Dashboard && formAccessDenialShowsMetadata(reason) {
		// Expose minimal metadata for denied-but-publicly-addressable forms
		// (e.g. password/auth/role-gated), but hide private/draft existence.
		if resp.Form, err = s.buildProtoFormMetadata(ctx, form, query.AcceptLanguage); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func formAccessDenialShowsMetadata(reason openv1.FormAccessReason) bool {
	switch reason {
	case openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
		openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED,
		openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC:
		return false
	default:
		return true
	}
}

// Package public owns the public Form access and submission boundary.
package public

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// FormService implements the public FormService
type FormService struct {
	openv1connect.UnimplementedFormServiceHandler
	db          *gorm.DB
	password    *crypto.PasswordHasher
	spiceDB     *auth.SpiceDBClient
	assets      formdomain.PublicAssets
	auditWriter domainaudit.Appender
}

type formShareTokenState struct {
	valid bool
}

// NewFormService creates a new public FormService
func NewFormService(
	db *gorm.DB,
	password *crypto.PasswordHasher,
	spiceDB *auth.SpiceDBClient,
	deps formdomain.Dependencies,
) *FormService {
	dependencycheck.New("PublicFormService").RequireNotNil(db, "db").RequireNotNil(password, "password").
		RequireNotNil(spiceDB, "spiceDB").RequireNotNil(deps.PublicAssets, "public assets").
		Validate()
	return &FormService{
		db: db, password: password, spiceDB: spiceDB, assets: deps.PublicAssets,
	}
}

// NewAuditedFormService wires the public submission boundary to the same
// durable Domain Audit transaction as the submission row.
func NewAuditedFormService(
	db *gorm.DB,
	password *crypto.PasswordHasher,
	spiceDB *auth.SpiceDBClient,
	auditWriter domainaudit.Appender,
	deps formdomain.Dependencies,
) *FormService {
	if auditWriter == nil {
		panic("public form domain audit writer is required")
	}
	service := NewFormService(db, password, spiceDB, deps)
	service.auditWriter = auditWriter
	return service
}

// CheckAccess evaluates the unified form access policy and returns structured reasons.
func (s *FormService) CheckAccess(
	ctx context.Context,
	req *connect.Request[openv1.CheckFormAccessRequest],
) (*connect.Response[openv1.CheckFormAccessResponse], error) {
	target := normalizeFormAccessTarget(req.Msg.Target)
	form, err := s.findFormBySlugOrID(ctx, req.Msg.Slug)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return connect.NewResponse(&openv1.CheckFormAccessResponse{
				Accessible: false,
				Reason:     openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
			}), nil
		}
		return nil, err
	}

	var shareTokenState formShareTokenState
	switch target {
	case openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD:
		shareTokenState = s.validateShareToken(
			ctx,
			form,
			req.Msg.ShareToken,
			req.Msg.SharePassword,
			managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD,
		)
		if !shareTokenState.valid {
			return connect.NewResponse(&openv1.CheckFormAccessResponse{
				Accessible: false,
				Reason:     openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
			}), nil
		}
	default:
		shareTokenState = s.validateShareToken(
			ctx,
			form,
			req.Msg.ShareToken,
			req.Msg.SharePassword,
			managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM,
		)
	}

	context := normalizeFormAccessContext(req.Msg.Context)
	if target == openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD {
		context = openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL
	}

	reason, err := s.evaluateFormAccess(ctx, form, formAccessOptions{
		context:                  context,
		hasValidPreviewToken:     shareTokenState.valid,
		bypassAuth:               shareTokenState.valid,
		bypassRoles:              shareTokenState.valid,
		enforcePassword:          !shareTokenState.valid,
		password:                 req.Msg.Password,
		checkSubmissionLimit:     target == openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM,
		checkDuplicateSubmission: target == openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM,
	})
	if err != nil {
		return nil, err
	}

	resp := &openv1.CheckFormAccessResponse{
		Accessible: reason == openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED,
		Reason:     reason,
	}
	if resp.Accessible {
		protoForm, err := s.buildProtoForm(ctx, form, req.Header().Get("Accept-Language"))
		if err != nil {
			return nil, err
		}
		resp.Form = protoForm
	} else if target == openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM &&
		reason != openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND &&
		reason != openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED &&
		reason != openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC {
		// Expose minimal metadata for denied-but-publicly-addressable forms
		// (e.g. password/auth/role-gated), but hide private/draft existence.
		metadata, err := s.buildProtoFormMetadata(ctx, form, req.Header().Get("Accept-Language"))
		if err != nil {
			return nil, err
		}
		resp.Form = metadata
	}

	return connect.NewResponse(resp), nil
}

// validateShareToken validates that the share token allows access to the form
// for one of the given share link entity types.

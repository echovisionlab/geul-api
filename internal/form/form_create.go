package form

import (
	"connectrpc.com/connect"
	"context"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

const formContentDocumentProfile = "compact"

func (s *FormService) CreateForm(
	ctx context.Context,
	req *connect.Request[managev1.CreateFormRequest],
) (*connect.Response[managev1.Form], error) {
	// Validate required fields
	title := strings.TrimSpace(req.Msg.Title)
	if title == "" {
		return nil, errs.Required("title")
	}
	normalizedSlug, err := s.normalizeNewFormSlug(ctx, req.Msg.Slug)
	if err != nil {
		return nil, err
	}
	if err := validateCanonicalFormSchema(req.Msg.Schema); err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	now := time.Now().UTC()
	form, err := s.newFormFromRequest(req.Msg, normalizedSlug, now)
	if err != nil {
		return nil, err
	}
	_, err = authzmutation.Execute(
		ctx,
		s.db,
		s.spiceDB,
		func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
			if err := s.requireFreshFormCreation(ctx, tx); err != nil {
				return err
			}
			if err := s.createFormWithDB(
				ctx, tx, form, title, req.Msg.Schema, req.Header().Get("Accept-Language"), now,
			); err != nil {
				return err
			}
			if err := s.appendFormCreatedAudit(ctx, tx, form.ID); err != nil {
				return err
			}
			policyTouch, err := policyv1.Form.TouchPolicy(form.ID)
			if err != nil {
				return err
			}
			policyDelete, err := policyv1.Form.DeletePolicy(form.ID)
			if err != nil {
				return err
			}
			return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
		},
	)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	protoForm, err := s.toProtoForm(ctx, form)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(protoForm), nil
}

func (s *FormService) normalizeNewFormSlug(ctx context.Context, requested *string) (*string, error) {
	if requested == nil || strings.TrimSpace(*requested) == "" {
		return nil, nil
	}
	slug := strings.TrimSpace(*requested)
	if err := validateSlugWithoutSlash(slug); err != nil {
		return nil, err
	}
	if err := ensureSlugAvailable(ctx, s.db, slug, ""); err != nil {
		return nil, err
	}
	if err := s.routes.EnsureAvailable(ctx, s.db, slug, ""); err != nil {
		return nil, err
	}
	return &slug, nil
}

func (s *FormService) newFormFromRequest(
	request *managev1.CreateFormRequest,
	slug *string,
	now time.Time,
) (*model.Form, error) {
	form := &model.Form{
		Slug: slug, Status: model.FormStatus(managev1.FormStatus_FORM_STATUS_DRAFT.String()),
		CreatedAt: now, RequireAuth: request.RequireAuth,
		AllowDuplicateSubmission: request.AllowDuplicateSubmission,
		MaxSubmissions:           request.MaxSubmissions,
	}
	if request.IsPublic != nil {
		form.IsPublic = *request.IsPublic
	}
	if request.Password != nil && *request.Password != "" {
		hash, err := s.password.Hash(*request.Password)
		if err != nil {
			return nil, errs.Internal(err)
		}
		form.AccessPassword = &hash
	}
	if request.OpensAt != nil {
		value := request.OpensAt.AsTime()
		form.OpensAt = &value
	}
	if request.ClosesAt != nil {
		value := request.ClosesAt.AsTime()
		form.ClosesAt = &value
	}
	if len(request.AllowedRoles) > 0 {
		roles, err := normalizeUserRoleList("allowed_roles", request.AllowedRoles)
		if err != nil {
			return nil, err
		}
		form.AllowedRoles = pq.StringArray(roles)
	}
	if err := validateFormSettings(form); err != nil {
		return nil, err
	}
	return form, nil
}

func (s *FormService) createFormWithDB(
	ctx context.Context,
	tx *gorm.DB,
	form *model.Form,
	title string,
	schema []byte,
	acceptLanguage string,
	now time.Time,
) error {
	if form.Slug != nil && *form.Slug != "" {
		if err := s.routes.EnsureAvailableLocked(ctx, tx, *form.Slug, ""); err != nil {
			return err
		}
	}
	sourceLocale := s.translation.ResolveInitialSourceLocale(ctx, tx, s.kratosClient, acceptLanguage)
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile: formContentDocumentProfile, SourceLocale: sourceLocale,
	})
	if err != nil {
		return err
	}
	form.ContentDocumentID = document.Document.ID.String()
	form.SourceLocale = sourceLocale
	if err := tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(form).Error; err != nil {
		return err
	}
	if err := createInitialFormSourceLocaleRow(
		ctx, tx, s.translation, form.ID, sourceLocale, title, schema, now,
	); err != nil {
		return err
	}
	_, err = s.og.Request(ctx, tx, form.ID, title, sourceLocale, false, "form_created")
	return err
}

// UpdateForm updates an existing form

package form

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const formContentDocumentProfile = "compact"

// CreateFormCommand creates a draft Form. AcceptLanguage picks its source
// locale when the creator has no preferred locale.
type CreateFormCommand struct {
	Title                    string
	Slug                     *string
	Schema                   []byte
	IsPublic                 *bool
	RequireAuth              *bool
	AllowDuplicateSubmission *bool
	Password                 *string
	MaxSubmissions           *int32
	OpensAt                  *time.Time
	ClosesAt                 *time.Time
	AllowedRoles             []string
	AcceptLanguage           string
}

// CreateForm creates a draft Form, its source locale document and its
// authorization policy.
func (s *Service) CreateForm(ctx context.Context, command CreateFormCommand) (View, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return View{}, domainerr.Required("title")
	}
	normalizedSlug, err := s.normalizeNewFormSlug(ctx, command.Slug)
	if err != nil {
		return View{}, err
	}
	if err := validateCanonicalFormSchema(command.Schema); err != nil {
		return View{}, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	now := dbtime.Now()
	form, err := s.newForm(command, normalizedSlug, now)
	if err != nil {
		return View{}, err
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[*Form, Store]{
		Access: s.freshFormCreation(),
		Change: func(ctx context.Context, store Store) (*Form, error) {
			return form, createForm(ctx, store, form, title, command.Schema, command.AcceptLanguage, now)
		},
		Audit: func(form *Form) mutation.Audit { return formCreatedAudit(form.ID) },
		Relationships: func(form *Form) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return formPolicyRelationships(form.ID, true)
		},
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, *created)
}

// formPolicyRelationships is the Form policy write of a creation (touch) or
// deletion, and its exact inverse.
func formPolicyRelationships(formID string, create bool) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	touch, err := policyv1.Form.TouchPolicy(formID)
	if err != nil {
		return nil, nil, err
	}
	remove, err := policyv1.Form.DeletePolicy(formID)
	if err != nil {
		return nil, nil, err
	}
	if create {
		return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
	}
	return []policyv1.RelationshipMutation{remove}, []policyv1.RelationshipMutation{touch}, nil
}

func (s *Service) normalizeNewFormSlug(ctx context.Context, requested *string) (*string, error) {
	if requested == nil || strings.TrimSpace(*requested) == "" {
		return nil, nil
	}
	slug := strings.TrimSpace(*requested)
	if err := pathslug.Validate(slug); err != nil {
		return nil, err
	}
	if err := s.units.Read(ctx, func(store Store) error {
		return ensureSlugAvailable(ctx, store, slug, "")
	}); err != nil {
		return nil, err
	}
	return &slug, nil
}

func (s *Service) newForm(command CreateFormCommand, slug *string, now time.Time) (*Form, error) {
	form := &Form{
		Slug: slug, Status: StatusDraft,
		CreatedAt: now, RequireAuth: command.RequireAuth,
		AllowDuplicateSubmission: command.AllowDuplicateSubmission,
		MaxSubmissions:           command.MaxSubmissions,
		OpensAt:                  command.OpensAt,
		ClosesAt:                 command.ClosesAt,
	}
	if command.IsPublic != nil {
		form.IsPublic = *command.IsPublic
	}
	if command.Password != nil && *command.Password != "" {
		hash, err := s.password.Hash(*command.Password)
		if err != nil {
			return nil, err
		}
		form.AccessPassword = &hash
	}
	if len(command.AllowedRoles) > 0 {
		roles, err := normalizeUserRoleList("allowed_roles", command.AllowedRoles)
		if err != nil {
			return nil, err
		}
		form.AllowedRoles = roles
	}
	if err := validateFormSettings(form); err != nil {
		return nil, err
	}
	return form, nil
}

func createForm(
	ctx context.Context,
	store Store,
	form *Form,
	title string,
	schema []byte,
	acceptLanguage string,
	now time.Time,
) error {
	if form.Slug != nil && *form.Slug != "" {
		if err := store.LockRoute(ctx, *form.Slug); err != nil {
			return err
		}
	}
	sourceLocale := resolveInitialSourceLocale(ctx, store, acceptLanguage)
	documentID, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile: formContentDocumentProfile, SourceLocale: sourceLocale,
	})
	if err != nil {
		return err
	}
	form.ContentDocumentID = documentID.String()
	form.SourceLocale = sourceLocale
	if err := store.InsertForm(ctx, form); err != nil {
		return err
	}
	if err := saveFormSourceLocaleDocumentState(ctx, store, form.ID, normalizeInitialSourceLocale(ctx, store, sourceLocale), TranslationDocumentSaveInput{
		Title: &title, ContentJSON: schema, Now: now,
	}); err != nil {
		return err
	}
	_, err = store.RequestOG(ctx, form.ID, sourceLocale, false, "form_created")
	return err
}

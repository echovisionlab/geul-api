package emailauthoring

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// TemplateEditorProjection is an Email Template's source document as the
// editor loads it.
type TemplateEditorProjection struct {
	Document     *contentv1.RichTextDocument
	Revision     string
	SourceLocale string
}

// customEmailTemplateVariables names the placeholders of a custom
// template's source HTML, sorted and without duplicates.
func customEmailTemplateVariables(contentHTML string) []TemplateVariable {
	matches := previewPlaceholderRegex.FindAllStringSubmatch(email.NormalizeTemplatePlaceholders(contentHTML), -1)
	seen := make(map[string]struct{}, len(matches))
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(match[1]))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	variables := make([]TemplateVariable, 0, len(names))
	for _, name := range names {
		variables = append(variables, TemplateVariable{Name: name})
	}
	return variables
}

func loadTemplateEditorProjection(ctx context.Context, store Store, templateID string) (TemplateEditorProjection, error) {
	documentID, err := loadTemplateContentDocument(ctx, store, templateID, false)
	if err != nil {
		return TemplateEditorProjection{}, err
	}
	sourceLocale, err := loadTemplateSourceLocale(ctx, store, templateID, false)
	if err != nil {
		return TemplateEditorProjection{}, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return TemplateEditorProjection{}, emailTemplateContent.NormalizeContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return TemplateEditorProjection{}, emailTemplateContent.NormalizeContentBlockError(err)
	}
	return TemplateEditorProjection{Document: document, Revision: snapshot.Document.Revision.String(), SourceLocale: sourceLocale}, nil
}

// requireEmailTemplateDocumentLoad authorizes the session of a room load to
// edit the template that no active delivery renders.
func (s *InternalService) requireEmailTemplateDocumentLoad(ctx context.Context, sessionID, templateID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return domainerr.AuthenticationRequired()
	}
	var resolved *auth.UserInfo
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if resolved, err = store.ResolveSessionPrincipal(ctx, sessionID); err != nil {
			return failurecause.WithPrivate(err, sessionID)
		}
		if resolved == nil || !resolved.Authenticated || resolved.Banned || !resolved.Onboarded {
			return nil
		}
		return store.RequireTemplateMutable(ctx, templateID)
	})
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	if err != nil {
		return err
	}
	if resolved == nil || !resolved.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if resolved.Banned {
		return domainerr.AccountBanned()
	}
	if !resolved.Onboarded {
		return domainerr.NoPermission("edit", "collaboration resource")
	}
	can, err := emailTemplateEditCan(templateID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical Email Template UUID")
	}
	authorizationCtx := auth.WithUser(ctx, resolved)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := s.spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return err
	}
	if !allowed {
		return domainerr.NoPermission("edit", "collaboration resource")
	}
	return nil
}

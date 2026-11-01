package emailauthoring

import (
	"context"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type emailTemplateEditorProjection struct {
	Document     *contentv1.RichTextDocument
	Revision     string
	SourceLocale string
}

func syncCustomEmailTemplateVariables(ctx context.Context, db *gorm.DB, templateID, contentHTML string) error {
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
	variables := make(model.EmailTemplateVariables, 0, len(names))
	for _, name := range names {
		variables = append(variables, model.EmailTemplateVariable{Name: name})
	}
	return db.WithContext(ctx).Model(&emailTemplateBaseRow{}).
		Where("id = ? AND is_system = FALSE", templateID).Update("variables", variables).Error
}

func loadEmailTemplateEditorProjection(ctx context.Context, db *gorm.DB, store *contentblock.Store, templateID string) (emailTemplateEditorProjection, error) {
	documentID, err := loadEmailTemplateContentDocument(ctx, db, templateID, false)
	if err != nil {
		return emailTemplateEditorProjection{}, err
	}
	domain, err := loadEmailTemplateSourceContext(ctx, db, templateID, false)
	if err != nil {
		return emailTemplateEditorProjection{}, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, domain.SourceLocale)
	if err != nil {
		return emailTemplateEditorProjection{}, emailTemplateContent.NormalizeContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return emailTemplateEditorProjection{}, emailTemplateContent.NormalizeContentBlockError(err)
	}
	return emailTemplateEditorProjection{Document: document, Revision: snapshot.Document.Revision.String(), SourceLocale: domain.SourceLocale}, nil
}

// requireEmailTemplateDocumentLoad authorizes the collaboration principal of
// a room load to edit the template, which no active delivery may render.
func requireEmailTemplateDocumentLoad(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	references CampaignDeliveryReferences,
	principal *intrav1.CollaborationPrincipal,
	templateID string,
) error {
	if principal == nil || strings.TrimSpace(principal.GetSessionId()) == "" {
		return domainerr.AuthenticationRequired()
	}
	resolved, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, db, principal.GetSessionId())
	if err != nil {
		// The boundary keeps *_id request fields; this one is a session secret.
		err = failurecause.WithPrivate(err, principal.GetSessionId())
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return domainerr.AuthenticationRequired().WithCause(err)
		}
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
	if err := references.RequireTemplateMutable(ctx, db, templateID); err != nil {
		return err
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
	allowed, err := spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return err
	}
	if !allowed {
		return domainerr.NoPermission("edit", "collaboration resource")
	}
	return nil
}

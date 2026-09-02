package emailauthoring

import (
	"context"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type campaignEmailEditorProjection struct {
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
	if err := db.WithContext(ctx).Model(&emailTemplateBaseRow{}).
		Where("id = ? AND is_system = FALSE", templateID).Update("variables", variables).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func loadCampaignEmailEditorProjection(ctx context.Context, db *gorm.DB, store *contentblock.Store, entityType, entityID string) (campaignEmailEditorProjection, error) {
	if store == nil {
		return campaignEmailEditorProjection{}, errs.Internal(errors.New("email template content block store is not configured"))
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, db, entityType, entityID)
	if err != nil {
		return campaignEmailEditorProjection{}, err
	}
	domain, err := loadCampaignEmailSourceContext(ctx, db, entityType, entityID)
	if err != nil {
		return campaignEmailEditorProjection{}, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, domain.SourceLocale)
	if err != nil {
		return campaignEmailEditorProjection{}, normalizeCampaignEmailContentBlockError(entityType, err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return campaignEmailEditorProjection{}, normalizeCampaignEmailContentBlockError(entityType, err)
	}
	return campaignEmailEditorProjection{Document: document, Revision: snapshot.Document.Revision.String(), SourceLocale: domain.SourceLocale}, nil
}

func requireCampaignEmailDocumentLoad(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	references CampaignDeliveryReferences,
	principal *intrav1.CollaborationPrincipal,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
) error {
	if principal == nil || strings.TrimSpace(principal.GetSessionId()) == "" {
		return errs.AuthenticationRequired()
	}
	resolved, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, db, principal.GetSessionId())
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) || resolved == nil || !resolved.Authenticated {
		return errs.AuthenticationRequired()
	}
	if err != nil {
		return errs.Internal(err)
	}
	if resolved.Banned {
		return errs.AccountBanned()
	}
	if !resolved.Onboarded {
		return errs.NoPermission("edit", "collaboration resource")
	}
	if resourceType != intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE {
		return errs.InternalMsg("unsupported Email Template collaboration resource")
	}
	if err := ensureEmailTemplateMutableForActiveDelivery(ctx, db, references, resourceID); err != nil {
		return err
	}
	can, err := emailTemplateEditCan(resourceID)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical Email Template UUID")
	}
	authorizationCtx := auth.WithUser(ctx, resolved)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return err
	}
	if !allowed {
		return errs.NoPermission("edit", "collaboration resource")
	}
	return nil
}

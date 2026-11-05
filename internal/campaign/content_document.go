package campaign

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	campaignContentEntity = "campaign"
	emailContentProfile   = "email"
)

// campaignContent is the Campaign owner of shared email content.
var campaignContent = emailcontent.Owner[Store]{
	Name: "Campaign", Entity: campaignContentEntity, IDField: "campaign_id",
	FileBlocksRejected:        "Campaign documents cannot contain File Blocks",
	TranslatedSubjectRequired: "translated Campaign subject is required",
	Rows:                      campaignContentRows{},
	LocaleContentAudit: emailcontent.LocaleContentAudit{
		Action: sharedtelemetry.AuditCampaignUpdated,
		Record: sharedtelemetry.NewCampaignLocaleContentAuditRecord,
	},
}

// campaignContentRows reads the Campaign rows beside its email content.
type campaignContentRows struct{}

func (campaignContentRows) DocumentID(ctx context.Context, store Store, id string) (uuid.UUID, error) {
	return loadCampaignEmailContentDocumentRoot(ctx, store, id, false)
}

func (campaignContentRows) SourceContext(ctx context.Context, store Store, id string, lock bool) (contentblock.DomainContext, error) {
	return loadCampaignSourceContext(ctx, store, id, lock)
}

// ProjectSource rebuilds the source locale's materialized content; a
// Campaign's targets keep theirs until they are written.
func (campaignContentRows) ProjectSource(ctx context.Context, store Store, id string, snapshot contentblock.Snapshot, _ bool, now time.Time) error {
	return projectCampaignEmailMaterializedContent(ctx, store, id, snapshot, []string{snapshot.SourceLocale}, now)
}

// loadCampaignEmailContentDocumentRoot reads the Campaign's content
// document, locking the Campaign row when lock is set.
func loadCampaignEmailContentDocumentRoot(ctx context.Context, store Store, entityID string, lock bool) (uuid.UUID, error) {
	document, err := store.CampaignDocumentID(ctx, entityID, lock)
	if errors.Is(err, ErrCampaignNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound(campaignContentEntity, entityID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if document == nil || strings.TrimSpace(*document) == "" {
		return uuid.Nil, domainerr.Precondition("Campaign content document is not initialized")
	}
	id, err := uuid.Parse(*document)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid Campaign content_document_id: %w", err)
	}
	return id, nil
}

func campaignEmailContentFence(store Store, entityID string) emailcontent.Fence {
	return func(ctx context.Context, documentID uuid.UUID) (contentblock.DomainContext, error) {
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, store, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Campaign content document ownership changed; reload before saving")
		}

		status, err := store.CampaignStatus(ctx, entityID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if !campaignStatusAllowsEdit(status) {
			return contentblock.DomainContext{}, domainerr.Precondition(msgCampaignCannotUpdateSent)
		}
		return loadCampaignSourceContext(ctx, store, entityID, true)
	}
}

func campaignEmailDeleteContentFence(store Store, entityID string) emailcontent.Fence {
	return func(ctx context.Context, documentID uuid.UUID) (contentblock.DomainContext, error) {
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, store, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Campaign content document ownership changed; reload before saving")
		}

		status, err := store.CampaignStatus(ctx, entityID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if CampaignStatus(status) != CampaignStatusDraft {
			return contentblock.DomainContext{}, domainerr.Precondition("only draft campaigns can be deleted")
		}
		return loadCampaignSourceContext(ctx, store, entityID, true)
	}
}

// requireCampaignEmailDocumentLoad authorizes the collaboration principal
// of a room load to edit the draft Campaign.
func requireCampaignEmailDocumentLoad(
	ctx context.Context,
	db Store,
	checker CollaborationPermissionChecker,
	principal *CollaborationPrincipal,
	resourceID string,
) error {
	if principal == nil || strings.TrimSpace(principal.SessionID) == "" {
		return domainerr.AuthenticationRequired()
	}
	resolved, err := db.ResolveSessionPrincipal(ctx, principal.SessionID)
	// The boundary keeps *_id request fields; this one is a session secret.
	err = failurecause.WithPrivate(err, principal.SessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	if err != nil {
		return fmt.Errorf("resolve Campaign collaboration principal: %w", err)
	}
	if resolved == nil || !resolved.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if resolved.Banned {
		return domainerr.AccountBanned()
	}
	if !resolved.Onboarded {
		return domainerr.NoPermission("edit", "campaign")
	}
	if err := requireCampaignEmailDocumentEditable(ctx, db, resourceID); err != nil {
		return err
	}
	can, err := campaignEditCan(resourceID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical Campaign UUID")
	}
	authorizationCtx := auth.WithUser(ctx, resolved)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(authorizationCtx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission(can.Action().Name(), "campaign")
	}
	return nil
}

func requireCampaignEmailDocumentEditable(ctx context.Context, store Store, entityID string) error {
	status, err := store.CampaignStatus(ctx, entityID)
	if errors.Is(err, ErrCampaignNotFound) {
		return domainerr.ResourceNotFound("campaign", entityID)
	}
	if err != nil {
		return err
	}
	if !campaignStatusAllowsEdit(status) {
		return domainerr.Precondition(msgCampaignCannotUpdateSent)
	}
	return nil
}

// loadCampaignSourceContext reads the Campaign's translation source locale,
// locking the Campaign row when lock is set.
func loadCampaignSourceContext(ctx context.Context, store Store, entityID string, lock bool) (contentblock.DomainContext, error) {
	locale, err := store.CampaignSourceLocale(ctx, entityID, lock)
	if errors.Is(err, ErrCampaignNotFound) {
		return contentblock.DomainContext{}, domainerr.ResourceNotFound(campaignContentEntity, entityID)
	}
	if err != nil {
		return contentblock.DomainContext{}, err
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("Campaign translation source is not initialized")
	}
	normalized, err := emailcontent.NormalizeLocale(locale)
	if err != nil {
		return contentblock.DomainContext{}, domainerr.Precondition("Campaign translation source locale is invalid").WithCause(failurecause.ServerFault(err))
	}
	return contentblock.DomainContext{SourceLocale: normalized}, nil
}

func loadCampaignEmailSourceSubject(ctx context.Context, store Store, entityID, sourceLocale string) (string, error) {
	subject, exists, err := store.CampaignLocaleSubject(ctx, entityID, sourceLocale, false)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", domainerr.Precondition("Campaign source locale metadata is not initialized")
	}
	if subject == nil || strings.TrimSpace(*subject) == "" {
		return "", domainerr.Precondition("Campaign source locale subject is not initialized")
	}
	return strings.TrimSpace(*subject), nil
}

func loadCampaignEmailLocaleMetadata(ctx context.Context, store Store, entityID string) ([]CampaignLocaleMetadata, string, error) {
	domain, err := loadCampaignSourceContext(ctx, store, entityID, false)
	if err != nil {
		return nil, "", err
	}

	locales, err := store.CampaignLocaleMetadata(ctx, entityID)
	if err != nil {
		return nil, "", err
	}

	return locales, domain.SourceLocale, nil
}

func updateCampaignEmailLocaleSubject(
	ctx context.Context,
	tx Store,
	entityID string,
	locale string,
	subject string,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	locale, err := emailcontent.NormalizeLocale(locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return contentblock.MetadataEffect{}, domainerr.Required("subject")
	}
	if len(subject) > 500 {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("subject", "must be at most 500 characters")
	}
	domain, err := loadCampaignSourceContext(ctx, tx, entityID, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if locale != domain.SourceLocale {
		return contentblock.MetadataEffect{}, domainerr.Precondition("target Campaign locale metadata is read-only")
	}

	current, exists, err := tx.CampaignLocaleSubject(ctx, entityID, locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists && current != nil && *current == subject {
		return contentblock.MetadataEffect{}, nil
	}
	if !exists {
		if err := tx.InsertSourceSubject(ctx, entityID, locale, subject, now); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	} else if err := tx.UpdateSourceSubject(ctx, entityID, locale, subject, now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if err := tx.SetSourceSubject(ctx, entityID, subject, now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func projectCampaignEmailMaterializedContent(
	ctx context.Context,
	tx Store,
	entityID string,
	snapshot contentblock.Snapshot,
	changedLocales []string,
	now time.Time,
) error {
	if snapshot.Document.Profile != emailContentProfile {
		return domainerr.Precondition("Campaign content document profile changed")
	}
	domain, err := loadCampaignSourceContext(ctx, tx, entityID, false)
	if err != nil {
		return err
	}
	if snapshot.SourceLocale != domain.SourceLocale {
		return domainerr.Precondition("Campaign translation source locale changed; reload before saving")
	}
	requested := make(map[string]struct{}, len(changedLocales))
	for _, locale := range changedLocales {
		if locale = strings.TrimSpace(locale); locale != "" {
			requested[locale] = struct{}{}
		}
	}
	if len(requested) == 0 {
		return errors.New("campaign materialized projection requires an exact locale")
	}
	locales := make([]string, 0, len(requested))
	for locale := range requested {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
		if err != nil {
			return campaignContent.NormalizeContentBlockError(err)
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
		if err != nil {
			return campaignContent.NormalizeContentBlockError(err)
		}

		exists, err := tx.ProjectLocale(ctx, entityID, locale, emailcontent.LocaleProjection{HTML: projection.HTML, Text: projection.Text, UpdatedAt: &now})
		if err != nil {
			return err
		}
		if !exists {
			return domainerr.Precondition("Campaign locale metadata is not initialized")
		}

	}
	return nil
}

package campaign

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
)

// EmailAuthoring adapts Email Layout reads and locks to Campaign's narrow
// authoring dependency.
type EmailAuthoring struct{}

func NewEmailAuthoring() EmailAuthoring { return EmailAuthoring{} }

func (EmailAuthoring) LockLayoutsForCampaign(
	ctx context.Context,
	tx *gorm.DB,
	layoutIDs ...string,
) (map[string]campaigndomain.CampaignLayoutReference, error) {
	unique := make(map[string]struct{}, len(layoutIDs))
	for _, layoutID := range layoutIDs {
		layoutID = strings.TrimSpace(layoutID)
		if layoutID != "" {
			unique[layoutID] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return map[string]campaigndomain.CampaignLayoutReference{}, nil
	}
	ordered := make([]string, 0, len(unique))
	for layoutID := range unique {
		ordered = append(ordered, layoutID)
	}
	sort.Strings(ordered)

	var layouts []campaigndomain.CampaignLayoutReference
	if err := tx.WithContext(ctx).
		Table("email_layout").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id, updated_at").
		Where("id IN ?", ordered).
		Order("id ASC").
		Find(&layouts).Error; err != nil {
		return nil, err
	}
	locked := make(map[string]campaigndomain.CampaignLayoutReference, len(layouts))
	for _, layout := range layouts {
		locked[layout.ID] = layout
	}
	return locked, nil
}

func (EmailAuthoring) LoadLayoutSnapshot(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
) ([]campaigndomain.CampaignLayoutLocaleSnapshot, error) {
	rows, err := email.LoadLayoutLocaleSnapshots(ctx, emailpostgres.NewStore(db), layoutID)
	if err != nil {
		return nil, err
	}
	snapshots := make([]campaigndomain.CampaignLayoutLocaleSnapshot, 0, len(rows))
	for _, row := range rows {
		snapshots = append(snapshots, campaigndomain.CampaignLayoutLocaleSnapshot{
			Locale:         row.Locale,
			HTMLContent:    stringValue(row.ContentHTML),
			IsSourceLocale: row.IsSourceLocale,
		})
	}
	return snapshots, nil
}

// EmailRendering adapts generic email rendering functions to Campaign's
// consumer-owned rendering capability.
type EmailRendering struct{}

func NewEmailRendering() EmailRendering { return EmailRendering{} }

func (EmailRendering) BuildRenderData(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	siteOrigin string,
	requestedLocale string,
	input map[string]string,
) map[string]string {
	return emaildelivery.BuildEmailRenderData(
		ctx, emaildeliverypostgres.NewStore(db), cdnDomain, siteOrigin, requestedLocale, input,
	)
}

func (EmailRendering) WrapWithLayout(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	requestedLocale string,
	content string,
	data map[string]string,
) (string, error) {
	wrapped, _, err := email.WrapWithLayoutForLocaleStrict(
		ctx, emailpostgres.NewStore(db), layoutID, requestedLocale, content, data,
	)
	return wrapped, err
}

// LiveEmailRenderer composes Campaign's current Content Block projection with
// generic Email variable and Layout rendering for non-durable test sends.
type LiveEmailRenderer struct {
	contentBlocks *contentblock.Store
}

func NewLiveEmailRenderer(contentBlocks *contentblock.Store) *LiveEmailRenderer {
	if contentBlocks == nil {
		panic("Campaign live email renderer requires a content Block store")
	}
	return &LiveEmailRenderer{contentBlocks: contentBlocks}
}

func (renderer *LiveEmailRenderer) RenderCampaignEmail(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
	requestedLocale string,
	data map[string]string,
) (*email.RenderedEmail, error) {
	var root model.Campaign
	if err := db.WithContext(ctx).First(&root, "id = ?", campaignID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("campaign not found: %s", campaignID)
		}
		return nil, fmt.Errorf("load Campaign email root: %w", err)
	}
	localized, campaignLocale, err := campaigndomain.ResolveLocalizedCampaign(
		ctx, campaignpostgres.NewStore(db, campaignpostgres.Ports{ContentBlocks: renderer.contentBlocks}), campaignpostgres.CampaignFromRow(root), requestedLocale,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve Campaign email locale: %w", err)
	}
	subject, err := email.RenderSubjectVarsStrict(localized.Subject, data)
	if err != nil {
		return nil, fmt.Errorf("render Campaign email subject: %w", err)
	}
	html := ""
	if localized.ContentHTML != nil {
		html = *localized.ContentHTML
	}
	data["subject"] = subject

	displayedLocale := campaignLocale
	layoutLocale := ""
	if localized.LayoutID != nil {
		html, layoutLocale, err = email.WrapWithLayoutForLocaleStrict(
			ctx, emailpostgres.NewStore(db), *localized.LayoutID, requestedLocale, html, data,
		)
		if err != nil {
			return nil, fmt.Errorf("render Campaign email layout: %w", err)
		}
		if layoutLocale != "" {
			displayedLocale = layoutLocale
		}
	}
	if localized.LayoutID == nil {
		html, err = email.RenderEmailHTML(html, "", data)
		if err != nil {
			return nil, fmt.Errorf("render Campaign email HTML: %w", err)
		}
	}
	html = email.NormalizeRenderedHTML(html)
	normalizedRequestedLocale := strings.TrimSpace(requestedLocale)
	if normalized := localization.NormalizeSupportedLocale(normalizedRequestedLocale); normalized != nil {
		normalizedRequestedLocale = *normalized
	}
	return &email.RenderedEmail{
		Subject:            subject,
		HTML:               html,
		Text:               email.StripHTML(html),
		DisplayedLocale:    displayedLocale,
		TemplateLocale:     campaignLocale,
		LayoutLocale:       layoutLocale,
		ResolvedByFallback: normalizedRequestedLocale != "" && campaignLocale != normalizedRequestedLocale,
		LayoutUsedFallback: normalizedRequestedLocale != "" && layoutLocale != "" && layoutLocale != normalizedRequestedLocale,
	}, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ campaignpostgres.CampaignEmailAuthoringPort = EmailAuthoring{}
var _ campaignpostgres.CampaignEmailRenderingPort = EmailRendering{}

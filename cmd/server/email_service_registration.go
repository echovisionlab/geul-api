package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	audienceadapter "github.com/echovisionlab/geul-api/internal/adapters/audience"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/audience"
	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mutation "github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) registerEmailAuthoring(collaborationRuntime *collaborationadapter.Runtime, emailAuthoringReferences *emailauthoringadapter.CampaignDeliveryReferences) (mutation.UnitOfWork[emailauthoring.Store], error) {
	deps := r.dependencies
	emailAuthoringRuntime := emailauthoringadapter.NewRuntime()
	emailAuthoringUnits, err := emailauthoringpostgres.NewUnits(deps.db, emailauthoringpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, References: emailAuthoringReferences, Members: memberadapter.ContributorMembers{},
		Checkpoints: collaborationRuntime.Checkpoints, Rendering: emailAuthoringRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize email authoring store: %w", err)
	}
	emailAuthoringService, err := emailauthoring.NewService(emailauthoring.Deps{
		Units: emailAuthoringUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		TestEmails: emailauthoringadapter.NewTestEmails(deps.workerPublisher), Locales: emailAuthoringRuntime,
		CDNDomain: deps.cfg.CDNURL, SiteOrigin: deps.cfg.SiteOrigin,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize email authoring service: %w", err)
	}
	emailTemplatePath, emailTemplateHandler := managev1connect.NewEmailTemplateServiceHandler(emailauthoring.NewTemplateConnectHandler(emailAuthoringService), r.manageOptions...)
	deps.mux.Handle(emailTemplatePath, emailTemplateHandler)
	slog.Info("Registered service", "path", emailTemplatePath)

	emailLayoutPath, emailLayoutHandler := managev1connect.NewEmailLayoutServiceHandler(emailauthoring.NewLayoutConnectHandler(emailAuthoringService), r.manageOptions...)
	deps.mux.Handle(emailLayoutPath, emailLayoutHandler)
	slog.Info("Registered service", "path", emailLayoutPath)

	return emailAuthoringUnits, nil
}

func (r serviceRegistration) registerEmailDelivery() (*emaildelivery.Service, error) {
	deps := r.dependencies
	mailAdapterService, err := emaildelivery.NewService(emaildelivery.Deps{
		Units:          emaildeliverypostgres.NewUnits(deps.db),
		SpiceDB:        deps.spicedbClient,
		Fence:          identitystate.PostgresFence{},
		Audit:          deps.telemetryWriter,
		Providers:      emaildeliveryadapter.MailProviders{},
		AdapterCache:   deps.adapterLoader,
		AdapterChanges: emaildeliveryadapter.NewMailAdapterChanges(deps.ogPublisher),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize email delivery service: %w", err)
	}
	mailAdapterPath, mailAdapterHandler := managev1connect.NewMailAdapterServiceHandler(emaildelivery.NewMailAdapterConnectHandler(mailAdapterService), r.manageOptions...)
	deps.mux.Handle(mailAdapterPath, mailAdapterHandler)
	slog.Info("Registered service", "path", mailAdapterPath)

	emailSuppressionPath, emailSuppressionHandler := managev1connect.NewEmailSuppressionServiceHandler(emaildelivery.NewEmailSuppressionConnectHandler(mailAdapterService), r.manageOptions...)
	deps.mux.Handle(emailSuppressionPath, emailSuppressionHandler)
	slog.Info("Registered service", "path", emailSuppressionPath)

	return mailAdapterService, nil
}

func (r serviceRegistration) registerCampaign(collaborationRuntime *collaborationadapter.Runtime) (campaign.Units, error) {
	deps := r.dependencies
	campaignRuntime := campaignadapter.NewRuntime(deps.workerPublisher, deps.telemetryWriter)

	campaignUnits, err := campaignpostgres.NewUnits(deps.db, campaignpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Publisher: campaignRuntime, Routes: campaignRuntime, SpiceDB: deps.spicedbClient,
		EmailAuthoring: campaignadapter.NewEmailAuthoring(), EmailRendering: campaignadapter.NewEmailRendering(), AudienceTargets: campaignadapter.NewAudienceTargets(),
		Members: memberadapter.ContributorMembers{}, Checkpoints: collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize campaign store: %w", err)
	}
	campaignService, err := campaign.NewService(campaign.Deps{
		Units: campaignUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter, Runtime: campaignRuntime, Clock: time.Now,
		CDNDomain: deps.cfg.CDNURL, SiteOrigin: deps.cfg.SiteOrigin,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize campaign service: %w", err)
	}
	campaignPath, campaignHandler := managev1connect.NewCampaignServiceHandler(campaign.NewConnectHandler(campaignService), r.manageOptions...)
	deps.mux.Handle(campaignPath, campaignHandler)
	slog.Info("Registered service", "path", campaignPath)

	return campaignUnits, nil
}

func (r serviceRegistration) registerAudience() error {
	deps := r.dependencies
	audienceService, err := audience.NewService(audience.Deps{
		Units:            audiencepostgres.NewUnits(deps.db, campaignpostgres.SegmentUsage{}),
		SpiceDB:          deps.spicedbClient,
		Fence:            identitystate.PostgresFence{},
		Audit:            deps.telemetryWriter,
		RecipientCounter: audienceadapter.NewRecipientCounter(deps.db, deps.spicedbClient),
	})
	if err != nil {
		return fmt.Errorf("initialize audience service: %w", err)
	}
	audiencePath, audienceHandler := managev1connect.NewAudienceServiceHandler(audience.NewConnectHandler(audienceService), r.manageOptions...)
	deps.mux.Handle(audiencePath, audienceHandler)
	slog.Info("Registered service", "path", audiencePath)

	return nil
}

func (r serviceRegistration) registerEmailProviderCallbacks() {
	deps := r.dependencies
	if strings.TrimSpace(deps.cfg.SESEventSNSTopicARN) != "" {
		providerNotifications := emaildelivery.NewProviderNotificationProcessor(
			emaildeliveryadapter.NewCampaignProviderOutcomeStore(deps.db),
			emaildeliveryadapter.NewSuppressionStore(deps.db),
		)
		sesEventHandler := emaildeliveryadapter.NewSESEventHandler(deps.cfg.SESEventSNSTopicARN, providerNotifications)
		deps.mux.Handle(emaildeliveryadapter.SESEventCallbackPath, sesEventHandler)
		slog.Info("Registered SES event callback", "path", emaildeliveryadapter.SESEventCallbackPath)
	}

}

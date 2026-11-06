package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	oauthappadapter "github.com/echovisionlab/geul-api/internal/adapters/oauthapp"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	membercriteriaadapter "github.com/echovisionlab/geul-api/internal/adapters/membercriteria"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	mutation "github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) registerEmailAuthoring(collaborationRuntime *collaborationadapter.Runtime, emailAuthoringReferences *emailauthoringadapter.CampaignDeliveryReferences) (mutation.UnitOfWork[emailauthoring.Store], *emailauthoring.Service, error) {
	deps := r.dependencies
	emailAuthoringRuntime := emailauthoringadapter.NewRuntime()
	emailAuthoringUnits, err := emailauthoringpostgres.NewUnits(deps.db, emailauthoringpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, References: emailAuthoringReferences, Members: memberadapter.ContributorMembers{},
		Checkpoints: collaborationRuntime.Checkpoints, Rendering: emailAuthoringRuntime,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize email authoring store: %w", err)
	}
	emailAuthoringService, err := emailauthoring.NewService(emailauthoring.Deps{
		Units: emailAuthoringUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		TestEmails: emailauthoringadapter.NewTestEmails(deps.workerPublisher), Locales: emailAuthoringRuntime,
		CDNDomain: deps.cfg.CDNURL, SiteOrigin: deps.cfg.SiteOrigin,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize email authoring service: %w", err)
	}
	emailTemplatePath, emailTemplateHandler := managev1connect.NewEmailTemplateServiceHandler(emailauthoring.NewTemplateConnectHandler(emailAuthoringService), r.manageOptions...)
	deps.mux.Handle(emailTemplatePath, emailTemplateHandler)
	slog.Info("Registered service", "path", emailTemplatePath)

	emailLayoutPath, emailLayoutHandler := managev1connect.NewEmailLayoutServiceHandler(emailauthoring.NewLayoutConnectHandler(emailAuthoringService), r.manageOptions...)
	deps.mux.Handle(emailLayoutPath, emailLayoutHandler)
	slog.Info("Registered service", "path", emailLayoutPath)

	return emailAuthoringUnits, emailAuthoringService, nil
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

func (r serviceRegistration) registerCampaign(collaborationRuntime *collaborationadapter.Runtime) (campaign.Units, *campaign.Service, error) {
	deps := r.dependencies
	campaignRuntime := campaignadapter.NewRuntime(deps.workerPublisher, deps.telemetryWriter)

	campaignUnits, err := campaignpostgres.NewUnits(deps.db, campaignpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Publisher: campaignRuntime, Routes: campaignRuntime, SpiceDB: deps.spicedbClient,
		EmailAuthoring: campaignadapter.NewEmailAuthoring(), EmailRendering: campaignadapter.NewEmailRendering(), CriteriaTargets: campaignadapter.NewCriteriaTargets(),
		Members: memberadapter.ContributorMembers{}, Checkpoints: collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize campaign store: %w", err)
	}
	campaignService, err := campaign.NewService(campaign.Deps{
		Units: campaignUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter, Runtime: campaignRuntime, Clock: time.Now,
		CDNDomain: deps.cfg.CDNURL, SiteOrigin: deps.cfg.SiteOrigin,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize campaign service: %w", err)
	}
	campaignPath, campaignHandler := managev1connect.NewCampaignServiceHandler(campaign.NewConnectHandler(campaignService), r.manageOptions...)
	deps.mux.Handle(campaignPath, campaignHandler)
	slog.Info("Registered service", "path", campaignPath)

	return campaignUnits, campaignService, nil
}

func (r serviceRegistration) registerMemberCriteria() (*membercriteria.Service, error) {
	deps := r.dependencies
	criteriaService, err := membercriteria.NewService(membercriteria.Deps{
		Units:            criteriapostgres.NewUnits(deps.db, campaignpostgres.CriteriaUsage{}, oauthappadapter.SignInAppUsage{}),
		SpiceDB:          deps.spicedbClient,
		Fence:            identitystate.PostgresFence{},
		Audit:            deps.telemetryWriter,
		RecipientCounter: membercriteriaadapter.NewRecipientCounter(deps.db, deps.spicedbClient),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize criteria service: %w", err)
	}
	criteriaPath, criteriaHandler := managev1connect.NewMemberCriteriaServiceHandler(membercriteria.NewConnectHandler(criteriaService), r.manageOptions...)
	deps.mux.Handle(criteriaPath, criteriaHandler)
	slog.Info("Registered service", "path", criteriaPath)

	return criteriaService, nil
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

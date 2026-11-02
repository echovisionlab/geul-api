package main

import (
	"fmt"
	"log/slog"

	mapthemeadapter "github.com/echovisionlab/geul-api/internal/adapters/maptheme"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	mapthemepostgres "github.com/echovisionlab/geul-api/internal/maptheme/postgres"
	mutation "github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) registerReferenceCatalog() (*referencecatalog.Service, error) {
	deps := r.dependencies
	referenceCatalogUnits, err := referencecatalogpostgres.NewUnits(deps.db, referencecatalogpostgres.Ports{
		DeletionReferences: referencecatalogadapter.DeletionReferences{},
		Assets:             referencecatalogadapter.NewAssets(deps.cfg.CDNURL),
		Members:            referencecatalogadapter.NewMemberSummaries(deps.cfg.CDNURL),
		MenuTargets:        referencecatalogmenuadapter.NewTargets(deps.telemetryWriter),
		ClientWorks:        workadapter.ClientWorks{},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize reference catalog store: %w", err)
	}
	referenceCatalog, err := referencecatalog.NewService(referencecatalog.Deps{
		Units: referenceCatalogUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize reference catalog service: %w", err)
	}
	return referenceCatalog, nil
}

func (r serviceRegistration) registerLegal(legalRuntime *legaladapter.Runtime, campaignDeliveryDispatcher *campaignadapter.DeliveryDispatcher, collaborationRuntime *collaborationadapter.Runtime) (mutation.UnitOfWork[legal.Store], *legal.Service, error) {
	deps := r.dependencies
	legalUnits, err := legalpostgres.NewUnits(deps.db, legalpostgres.Ports{
		ContentBlocks: deps.contentBlockStore,
		Routes:        legalRuntime,
		Notices:       legaladapter.NewNoticeRuntime(),
		PreviewLinks:  sharelinkadapter.LegalPreviewLinks{},
		Members:       memberadapter.PreferredLocales{},
		Checkpoints:   collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize legal store: %w", err)
	}
	legalPolicyService, err := legal.NewService(legal.Deps{
		Units:    legalUnits,
		SpiceDB:  deps.spicedbClient,
		Fence:    identitystate.PostgresFence{},
		Audit:    deps.telemetryWriter,
		Dispatch: legaladapter.NewNoticeDispatch(deps.db, campaignDeliveryDispatcher),
		OGAssets: legaladapter.NewOGAssets(deps.db, deps.cfg.CDNURL),
		BaseURL:  deps.cfg.SiteOrigin,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize legal policy service: %w", err)
	}
	privacyPath, privacyHandler := managev1connect.NewPrivacyServiceHandler(legal.NewPrivacyConnectHandler(legalPolicyService), r.manageOptions...)
	deps.mux.Handle(privacyPath, privacyHandler)
	slog.Info("Registered service", "path", privacyPath)

	termsPath, termsHandler := managev1connect.NewTermsServiceHandler(legal.NewTermsConnectHandler(legalPolicyService), r.manageOptions...)
	deps.mux.Handle(termsPath, termsHandler)
	slog.Info("Registered service", "path", termsPath)

	return legalUnits, legalPolicyService, nil
}

func (r serviceRegistration) registerMapTheme() (*maptheme.Service, error) {
	deps := r.dependencies
	mapThemeService, err := maptheme.NewService(maptheme.Deps{
		Units: mapthemepostgres.NewUnits(deps.db, mapthemepostgres.Ports{SiteSettings: mapthemeadapter.SiteSettings{}}), SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize map theme service: %w", err)
	}
	mapThemePath, mapThemeHandler := managev1connect.NewMapThemeServiceHandler(maptheme.NewConnectHandler(mapThemeService), r.manageOptions...)
	deps.mux.Handle(mapThemePath, mapThemeHandler)
	slog.Info("Registered service", "path", mapThemePath)

	return mapThemeService, nil
}

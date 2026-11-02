package main

import (
	"fmt"
	"log/slog"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	ogadapter "github.com/echovisionlab/geul-api/internal/adapters/og"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/admin"
	adminpostgres "github.com/echovisionlab/geul-api/internal/admin/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) registerSiteSettings() (*sitesettings.Service, error) {
	deps := r.dependencies
	siteSettingUnits, err := sitesettingspostgres.NewUnits(deps.db, sitesettingspostgres.Ports{
		Assets: sitesettingsadapter.NewAssets(deps.cfg.CDNURL), References: sitesettingsadapter.NewReferences(), OG: deps.og.siteInvalidator(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize site settings store: %w", err)
	}
	siteSettingService, err := sitesettings.NewService(sitesettings.Deps{
		Assets: sitesettingspostgres.NewAssetReader(deps.db, sitesettingsadapter.NewAssets(deps.cfg.CDNURL)),
		Units:  siteSettingUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		SiteOrigin: deps.cfg.SiteOrigin,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize site settings service: %w", err)
	}
	siteSettingPath, siteSettingHandler := managev1connect.NewSiteSettingServiceHandler(sitesettings.NewConnectHandler(siteSettingService), r.manageOptions...)
	deps.mux.Handle(siteSettingPath, siteSettingHandler)
	slog.Info("Registered service", "path", siteSettingPath)

	return siteSettingService, nil
}

func (r serviceRegistration) registerAdmin() error {
	deps := r.dependencies
	ogAdmin := og.NewAdminConnectHandler(og.NewAdmin(deps.og.units, ogadapter.NewAuthorization(deps.spicedbClient)))
	adminUnits, err := adminpostgres.NewUnits(deps.db, adminpostgres.Ports{
		Members: memberadapter.DashboardMembers{}, Posts: postadapter.DashboardPosts{},
		Pages: pageadapter.DashboardPages{}, Campaigns: campaignadapter.DashboardCampaigns{},
	})
	if err != nil {
		return fmt.Errorf("initialize admin store: %w", err)
	}
	adminService, err := admin.NewService(admin.Deps{Units: adminUnits, SpiceDB: deps.spicedbClient})
	if err != nil {
		return fmt.Errorf("initialize admin service: %w", err)
	}
	adminPath, adminHandler := managev1connect.NewAdminServiceHandler(admin.NewConnectHandler(adminService, ogAdmin), r.manageOptions...)
	deps.mux.Handle(adminPath, adminHandler)
	slog.Info("Registered service", "path", adminPath)

	return nil
}

func (r serviceRegistration) registerShareLinks() error {
	deps := r.dependencies
	shareLinkService, err := sharelink.NewService(sharelink.Deps{
		Units: sharelinkpostgres.NewUnits(deps.db), SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
	})
	if err != nil {
		return err
	}
	shareLinkPath, shareLinkHandler := managev1connect.NewShareLinkServiceHandler(sharelink.NewConnectHandler(shareLinkService), r.manageOptions...)
	deps.mux.Handle(shareLinkPath, shareLinkHandler)
	slog.Info("Registered service", "path", shareLinkPath)

	return nil
}

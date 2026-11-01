package public

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sitelogo"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var linkTypeMap = map[string]openv1.LinkType{
	"custom":   openv1.LinkType_LINK_TYPE_CUSTOM,
	"page":     openv1.LinkType_LINK_TYPE_PAGE,
	"category": openv1.LinkType_LINK_TYPE_CATEGORY,
	"tag":      openv1.LinkType_LINK_TYPE_TAG,
	"series":   openv1.LinkType_LINK_TYPE_SERIES,
}

type userContext struct {
	isAuthenticated bool
	rolePermissions map[string]bool
}

// Deps are the collaborators of the public Site Settings use cases. Every
// field except SiteOrigin is required; NewService rejects a missing one.
type Deps struct {
	Projection ManifestProjection
	SpiceDB    *auth.SpiceDBClient
	// SiteOrigin resolves site variables and is reported to clients; empty
	// reports none.
	SiteOrigin string
}

// Service serves the public Site Settings singleton and its selected
// navigation menus.
type Service struct {
	siteOrigin string
	projection ManifestProjection
	spiceDB    *auth.SpiceDBClient
}

// NewService validates deps and returns the public Site Settings use cases.
func NewService(deps Deps) (*Service, error) {
	var missing []string
	if deps.Projection == nil {
		missing = append(missing, "Projection")
	}
	if deps.SpiceDB == nil {
		missing = append(missing, "SpiceDB")
	}
	if len(missing) != 0 {
		return nil, errors.New("public site settings dependencies are required: " + strings.Join(missing, ", "))
	}
	return &Service{
		siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
		projection: deps.Projection, spiceDB: deps.SpiceDB,
	}, nil
}

// GetManifestQuery reads the public manifest. AcceptLanguage localizes the
// selected menus.
type GetManifestQuery struct {
	AcceptLanguage string
}

// GetManifest returns the public settings and the menus visible to the
// caller.
func (s *Service) GetManifest(ctx context.Context, query GetManifestQuery) (*openv1.GetResponse, error) {
	user, err := s.getUserContext(ctx)
	if err != nil {
		return nil, err
	}
	settingsRow, err := s.projection.Settings(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.buildSettings(ctx, settingsRow)
	if err != nil {
		return nil, err
	}
	menus, err := s.loadMenus(ctx, user, settingsRow, query.AcceptLanguage)
	if err != nil {
		return nil, err
	}
	return &openv1.GetResponse{Settings: settings, Menus: menus}, nil
}

func hasAuthenticatedAccountIdentity(ctx context.Context) bool {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false
	}
	return true
}

func (s *Service) getUserContext(ctx context.Context) (userContext, error) {
	if !hasAuthenticatedAccountIdentity(ctx) {
		return userContext{}, nil
	}
	permissions := map[string]bool{}
	adminCan, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return userContext{}, err
	}
	admin, err := s.checkCurrentPlatformCan(ctx, adminCan)
	if err != nil {
		return userContext{}, err
	}
	if admin {
		permissions["admin"], permissions["author"], permissions["user"] = true, true, true
		return userContext{isAuthenticated: true, rolePermissions: permissions}, nil
	}
	authorCan, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return userContext{}, err
	}
	author, err := s.checkCurrentPlatformCan(ctx, authorCan)
	if err != nil {
		return userContext{}, err
	}
	if author {
		permissions["author"], permissions["user"] = true, true
		return userContext{isAuthenticated: true, rolePermissions: permissions}, nil
	}
	userCan, err := policyv1.Platform.IsUser()
	if err != nil {
		return userContext{}, err
	}
	user, err := s.checkCurrentPlatformCan(ctx, userCan)
	if err != nil {
		return userContext{}, err
	}
	permissions["user"] = user
	return userContext{isAuthenticated: true, rolePermissions: permissions}, nil
}

func (s *Service) checkCurrentPlatformCan(ctx context.Context, can policyv1.Can) (bool, error) {
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return s.spiceDB.Can(ctx, decision)
}

func (s *Service) buildSettings(ctx context.Context, settings *model.SiteSettings) (*openv1.SiteSettings, error) {
	result := &openv1.SiteSettings{
		SiteTitle: settings.SiteTitle, MetaDescription: sitevariable.FromSettings(*settings, s.siteOrigin).ResolveText(settings.MetaDescription),
		PrimaryColor: settings.PrimaryColor, CompanyName: settings.CompanyName,
		CompanyAddress: settings.CompanyAddress, TaxId: settings.TaxID,
		LegalEmail: settings.LegalEmail, ContactEmail: settings.ContactEmail,
		SiteOrigin:             s.siteOrigin,
		DefaultCommentsEnabled: settings.DefaultCommentsEnabled, SocialLinks: map[string]string{},
	}
	if settings.GoogleAnalyticsID != nil && *settings.GoogleAnalyticsID != "" {
		result.GoogleAnalyticsId = settings.GoogleAnalyticsID
	}
	logos := sitelogo.Display[commonv1.AssetRef]{
		FullLight:   s.readyLogo(ctx, settings.LogoFullLightFileID),
		FullDark:    s.readyLogo(ctx, settings.LogoFullDarkFileID),
		SymbolLight: s.readyLogo(ctx, settings.LogoSymbolLightFileID),
		SymbolDark:  s.readyLogo(ctx, settings.LogoSymbolDarkFileID),
	}.Resolved()
	result.LogoFullLightAsset, result.LogoFullDarkAsset = logos.FullLight, logos.FullDark
	result.LogoSymbolLightAsset, result.LogoSymbolDarkAsset = logos.SymbolLight, logos.SymbolDark
	if settings.FaviconFileID != nil {
		result.FaviconAsset, result.FaviconAssetSet = s.projection.Favicon(ctx, *settings.FaviconFileID)
	}
	if settings.SiteOgAssetID != nil && strings.TrimSpace(*settings.SiteOgAssetID) != "" {
		result.SiteOgAsset = s.projection.ReadyAsset(ctx, *settings.SiteOgAssetID)
	}
	loaderAssets, err := s.projection.LoaderAssets(ctx)
	if err != nil {
		return nil, err
	}
	result.LoaderAssets = loaderAssets
	result.SocialLinks = manifestSocialLinks(ctx, settings.SocialLinks)
	return result, nil
}

func manifestSocialLinks(ctx context.Context, raw []byte) map[string]string {
	result := make(map[string]string)
	if len(raw) == 0 {
		return result
	}
	var values structured.Fields
	if err := json.Unmarshal(raw, &values); err != nil {
		slog.ErrorContext(ctx, "Stored social_links site setting is not valid JSON; serving no social links", "domain", "sitesettings", "operation", "decode_stored_setting", "outcome", "failed", "key", "social_links", failurecause.Attr(err))
		return result
	}
	for key, value := range values {
		if link, ok := value.(string); ok {
			result[key] = link
		}
	}
	return result
}

func (s *Service) loadMenus(
	ctx context.Context,
	user userContext,
	settings *model.SiteSettings,
	acceptLanguage string,
) (*openv1.Menus, error) {
	menus, err := s.projection.Menus(ctx, settings, acceptLanguage)
	if err != nil {
		return nil, err
	}
	return &openv1.Menus{
		Header:         s.filterAndConvertItems(ctx, menus.Header, user),
		Secondary:      s.filterAndConvertItems(ctx, menus.Secondary, user),
		Footer:         s.filterAndConvertItems(ctx, menus.Footer, user),
		AvatarDropdown: s.filterAndConvertItems(ctx, menus.AvatarDropdown, user),
	}, nil
}

func (s *Service) filterAndConvertItems(ctx context.Context, items []model.MenuItem, user userContext) []*openv1.MenuItem {
	result := make([]*openv1.MenuItem, 0, len(items))
	for i := range items {
		if s.isItemVisible(&items[i], user) {
			result = append(result, s.convertItem(ctx, &items[i], user))
		}
	}
	return result
}

func (s *Service) isItemVisible(item *model.MenuItem, user userContext) bool {
	if item.Visibility == nil {
		return true
	}
	switch item.Visibility.Mode {
	case "all", "":
		return true
	case "authenticated":
		return user.isAuthenticated
	case "guest":
		return !user.isAuthenticated
	case "roles":
		if !user.isAuthenticated {
			return false
		}
		for _, role := range item.Visibility.Roles {
			if user.rolePermissions[strings.ToLower(strings.TrimSpace(role))] {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func (s *Service) convertItem(ctx context.Context, item *model.MenuItem, user userContext) *openv1.MenuItem {
	result := &openv1.MenuItem{Id: item.ID, Label: item.Label, LinkType: linkTypeMap[item.LinkType]}
	result.Url, result.TargetId = item.URL, item.TargetID
	if slug := s.projection.TargetSlug(ctx, item); slug != nil {
		result.TargetSlug = slug
	}
	if item.OpenInNewTab != nil {
		result.OpenInNewTab = *item.OpenInNewTab
	}
	if len(item.Children) > 0 {
		result.Children = s.filterAndConvertItems(ctx, item.Children, user)
	}
	return result
}

// readyLogo returns the ready display asset for a configured logo slot, or nil
// when the slot is unset or its asset is not ready, so resolution falls back.
func (s *Service) readyLogo(ctx context.Context, fileID *string) *commonv1.AssetRef {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	return s.projection.ReadySourceAsset(ctx, *fileID, "logo")
}

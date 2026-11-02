package public

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sitelogo"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type userContext struct {
	isAuthenticated bool
	rolePermissions map[string]bool
}

// Manifest is the public settings and the menus visible to the caller.
type Manifest struct {
	Settings PublicSettings
	Menus    Menus
}

// PublicSettings are the settings every visitor reads. Each display logo
// falls back to another configured one; GoogleAnalyticsID is nil when unset.
type PublicSettings struct {
	SiteTitle              string
	MetaDescription        string
	PrimaryColor           string
	CompanyName            string
	CompanyAddress         string
	TaxID                  string
	LegalEmail             string
	ContactEmail           string
	SiteOrigin             string
	DefaultCommentsEnabled bool
	SocialLinks            map[string]string
	GoogleAnalyticsID      *string
	LogoFullLight          *sitesettings.Asset
	LogoFullDark           *sitesettings.Asset
	LogoSymbolLight        *sitesettings.Asset
	LogoSymbolDark         *sitesettings.Asset
	Favicon                *sitesettings.Asset
	FaviconSet             *sitesettings.FaviconSet
	SiteOg                 *sitesettings.Asset
	LoaderAssets           []sitesettings.Asset
}

// Menus are the visible items of the selected menus.
type Menus struct {
	Header         []MenuLink
	Secondary      []MenuLink
	Footer         []MenuLink
	AvatarDropdown []MenuLink
}

// MenuLink is a visible Menu item and its visible children.
type MenuLink struct {
	ID           string
	Label        string
	LinkType     string
	URL          *string
	TargetID     *string
	TargetSlug   *string
	OpenInNewTab bool
	Children     []MenuLink
}

// Deps are the collaborators of the public Site Settings use cases. Every
// field except SiteOrigin is required; NewService rejects a missing one.
type Deps struct {
	Projection ManifestProjection
	SpiceDB    authz.AuthorizationDecisionChecker
	// SiteOrigin resolves site variables and is reported to clients; empty
	// reports none.
	SiteOrigin string
}

// Service serves the public Site Settings singleton and its selected
// navigation menus.
type Service struct {
	siteOrigin string
	projection ManifestProjection
	spiceDB    authz.AuthorizationDecisionChecker
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
func (s *Service) GetManifest(ctx context.Context, query GetManifestQuery) (Manifest, error) {
	user, err := s.getUserContext(ctx)
	if err != nil {
		return Manifest{}, err
	}
	stored, err := s.projection.Settings(ctx)
	if err != nil {
		return Manifest{}, err
	}
	settings, err := s.buildSettings(ctx, stored)
	if err != nil {
		return Manifest{}, err
	}
	menus, err := s.loadMenus(ctx, user, stored, query.AcceptLanguage)
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{Settings: settings, Menus: menus}, nil
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

func (s *Service) buildSettings(ctx context.Context, settings sitesettings.Settings) (PublicSettings, error) {
	variables := sitevariable.Context{
		SiteTitle: settings.SiteTitle, SiteURL: strings.TrimRight(strings.TrimSpace(s.siteOrigin), "/"), CompanyName: settings.CompanyName,
		LegalEmail: settings.LegalEmail, ContactEmail: settings.ContactEmail,
	}
	result := PublicSettings{
		SiteTitle: settings.SiteTitle, MetaDescription: variables.ResolveText(settings.MetaDescription),
		PrimaryColor: settings.PrimaryColor, CompanyName: settings.CompanyName,
		CompanyAddress: settings.CompanyAddress, TaxID: settings.TaxID,
		LegalEmail: settings.LegalEmail, ContactEmail: settings.ContactEmail,
		SiteOrigin:             s.siteOrigin,
		DefaultCommentsEnabled: settings.DefaultCommentsEnabled,
	}
	if settings.GoogleAnalyticsID != nil && *settings.GoogleAnalyticsID != "" {
		result.GoogleAnalyticsID = settings.GoogleAnalyticsID
	}
	logos := sitelogo.Display[sitesettings.Asset]{
		FullLight:   s.readyLogo(ctx, settings.LogoFullLightFileID),
		FullDark:    s.readyLogo(ctx, settings.LogoFullDarkFileID),
		SymbolLight: s.readyLogo(ctx, settings.LogoSymbolLightFileID),
		SymbolDark:  s.readyLogo(ctx, settings.LogoSymbolDarkFileID),
	}.Resolved()
	result.LogoFullLight, result.LogoFullDark = logos.FullLight, logos.FullDark
	result.LogoSymbolLight, result.LogoSymbolDark = logos.SymbolLight, logos.SymbolDark
	if settings.FaviconFileID != nil {
		result.Favicon, result.FaviconSet = s.projection.Favicon(ctx, *settings.FaviconFileID)
	}
	if settings.SiteOgAssetID != nil && strings.TrimSpace(*settings.SiteOgAssetID) != "" {
		result.SiteOg = s.projection.ReadyAsset(ctx, *settings.SiteOgAssetID)
	}
	loaderAssets, err := s.projection.LoaderAssets(ctx)
	if err != nil {
		return PublicSettings{}, err
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

func (s *Service) loadMenus(ctx context.Context, user userContext, settings sitesettings.Settings, acceptLanguage string) (Menus, error) {
	menus, err := s.projection.Menus(ctx, settings, acceptLanguage)
	if err != nil {
		return Menus{}, err
	}
	return Menus{
		Header:         s.filterAndConvertItems(ctx, menus.Header, user),
		Secondary:      s.filterAndConvertItems(ctx, menus.Secondary, user),
		Footer:         s.filterAndConvertItems(ctx, menus.Footer, user),
		AvatarDropdown: s.filterAndConvertItems(ctx, menus.AvatarDropdown, user),
	}, nil
}

func (s *Service) filterAndConvertItems(ctx context.Context, items []MenuItem, user userContext) []MenuLink {
	result := make([]MenuLink, 0, len(items))
	for i := range items {
		if s.isItemVisible(&items[i], user) {
			result = append(result, s.convertItem(ctx, items[i], user))
		}
	}
	return result
}

func (s *Service) isItemVisible(item *MenuItem, user userContext) bool {
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

func (s *Service) convertItem(ctx context.Context, item MenuItem, user userContext) MenuLink {
	result := MenuLink{ID: item.ID, Label: item.Label, LinkType: item.LinkType, URL: item.URL, TargetID: item.TargetID}
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
func (s *Service) readyLogo(ctx context.Context, fileID *string) *sitesettings.Asset {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	return s.projection.ReadySourceAsset(ctx, *fileID, "logo")
}

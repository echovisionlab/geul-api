package filemedia

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type stubLegalRouteIdentity map[string]string

func (identity stubLegalRouteIdentity) RouteID(kind string) string { return identity[kind] }

func TestResolveSiteUploadEntityIDUsesBackendOwnedSlots(t *testing.T) {
	t.Parallel()
	legalRoutes := stubLegalRouteIdentity{
		"privacy": "00000000-0000-0000-0000-000000000301",
		"terms":   "00000000-0000-0000-0000-000000000302",
	}

	tests := []struct {
		name       string
		uploadType managev1.UploadType
		slotID     string
		want       string
	}{
		{name: "full light logo", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slotID: "logo_full_light", want: siteUploadEntityIDLogoFullLight},
		{name: "full dark logo", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slotID: "logo_full_dark", want: siteUploadEntityIDLogoFullDark},
		{name: "symbol light logo", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slotID: "logo_symbol_light", want: siteUploadEntityIDLogoSymbolLight},
		{name: "symbol dark logo", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slotID: "logo_symbol_dark", want: siteUploadEntityIDLogoSymbolDark},
		{name: "email light logo", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slotID: "logo_email_light", want: siteUploadEntityIDLogoEmailLight},
		{name: "email dark logo", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slotID: "logo_email_dark", want: siteUploadEntityIDLogoEmailDark},
		{
			name:       "loader",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOADER,
			slotID:     "loader",
			want:       siteUploadEntityIDLoader,
		},
		{
			name:       "site OG background",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
			slotID:     "site_og_background",
			want:       siteUploadEntityIDSiteOgBackground,
		},
		{
			name:       "privacy OG background",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
			slotID:     "privacy_og_background",
			want:       legalRoutes.RouteID("privacy"),
		},
		{
			name:       "terms OG background",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
			slotID:     "terms_og_background",
			want:       legalRoutes.RouteID("terms"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveSiteUploadEntityID(legalRoutes, tt.uploadType, tt.slotID)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveSiteUploadEntityIDRejectsUnsupportedSlot(t *testing.T) {
	t.Parallel()

	for _, slot := range []string{"", "logo", "logo_light", "logo_dark", "logo_email"} {
		_, err := resolveSiteUploadEntityID(nil, managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, slot)
		require.Error(t, err, slot)
	}

	_, err := resolveSiteUploadEntityID(
		nil,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
		"post_featured_image",
	)
	require.Error(t, err)
}

func TestResolveSiteUploadEntityIDRequiresLegalRouteIdentityForLegalSlots(t *testing.T) {
	t.Parallel()

	_, err := resolveSiteUploadEntityID(
		nil,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
		"privacy_og_background",
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestBuildManagedFileKeyUsesCanonicalMediaKeyForPageFeaturedImage(t *testing.T) {
	t.Parallel()

	service := &FileService{}

	fileID := "11111111-1111-4111-8111-111111111111"
	got, err := service.buildManagedFileKey(fileID, "image/webp")

	require.NoError(t, err)
	require.Equal(t, "media/"+fileID+".webp", got)
}

func TestBuildManagedFileKeyUsesCanonicalSiteEmailLogoKey(t *testing.T) {
	t.Parallel()

	service := &FileService{}

	fileID := "11111111-1111-4111-8111-111111111111"
	got, err := service.buildManagedFileKey(fileID, siteEmailLogoStableMime)

	require.NoError(t, err)
	require.Equal(t, "media/"+fileID+".png", got)
}

func TestValidateSiteEmailLogoMimeRequiresPNGOnlyForEmailLogoSlot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		uploadType managev1.UploadType
		slotID     string
		mimeType   string
		wantErr    bool
	}{
		{
			name:       "email light logo png",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			slotID:     siteLogoSlotEmailLight,
			mimeType:   siteEmailLogoStableMime,
		},
		{
			name:       "email dark logo rejects svg",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			slotID:     siteLogoSlotEmailDark,
			mimeType:   "image/svg+xml",
			wantErr:    true,
		},
		{
			name:       "email logo normalizes content type parameters",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			slotID:     siteLogoSlotEmailLight,
			mimeType:   " IMAGE/PNG ; charset=binary",
		},
		{
			name:       "email logo rejects svg",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			slotID:     siteLogoSlotEmailLight,
			mimeType:   "image/svg+xml",
			wantErr:    true,
		},
		{
			name:       "site full and symbol logos are not email logo restricted",
			uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			slotID:     siteLogoSlotSymbolDark,
			mimeType:   "image/svg+xml",
		},
		{
			name:       "client logo is not email logo restricted",
			uploadType: managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO,
			slotID:     siteLogoSlotEmailLight,
			mimeType:   "image/svg+xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSiteEmailLogoMime(tt.uploadType, tt.slotID, tt.mimeType)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "email logo uploads must be PNG")
				return
			}
			require.NoError(t, err)
		})
	}
}

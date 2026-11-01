package emaildelivery_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/emaildelivery/emaildeliverytest"
	"github.com/echovisionlab/geul-api/internal/sitelogo"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestRecipientLocaleMatchesTheNormalizedAddress(t *testing.T) {
	store := emaildeliverytest.NewStore()
	store.SeedRecipientLocale("reader@example.test", "pt")

	require.Equal(t, "pt-BR", emaildelivery.LookupEmailRecipientLocale(t.Context(), store, " Reader@Example.Test "))
	require.Empty(t, emaildelivery.LookupEmailRecipientLocale(t.Context(), store, "other@example.test"))
	require.Empty(t, emaildelivery.LookupEmailRecipientLocale(t.Context(), store, "  "))

	store.SeedRecipientLocale("reader@example.test", "unsupported")
	require.Empty(t, emaildelivery.LookupEmailRecipientLocale(t.Context(), store, "reader@example.test"))
}

func TestIdentityLocaleServesAPendingVerificationAddress(t *testing.T) {
	store := emaildeliverytest.NewStore()
	store.SeedRecipientLocale("current@example.test", "en")
	store.SeedIdentityLocale("identity-1", "ko")
	pendingVerification := &managev1.SendEmailEvent{
		Recipient: "pending@example.test",
		RecipientContext: &managev1.SendEmailEvent_AccountVerification{
			AccountVerification: &managev1.AccountVerificationRecipient{IdentityId: "identity-1"},
		},
	}

	require.Empty(t, emaildelivery.LookupEmailRecipientLocale(t.Context(), store, pendingVerification.GetRecipient()),
		"no Member uses the pending address yet")
	require.Equal(t, "ko", emaildelivery.LookupEmailIdentityLocale(t.Context(), store, emaildelivery.EmailRecipientContextIdentityID(pendingVerification)))
	require.Empty(t, emaildelivery.LookupEmailIdentityLocale(t.Context(), store, " "))
}

func TestLocaleLookupFailureFallsBackWithoutTheRecipient(t *testing.T) {
	logs := captureLogs(t)
	store := emaildeliverytest.NewStore()
	store.Fail = map[string]error{"RecipientLocale": errors.New("database unavailable"), "IdentityLocale": errors.New("database unavailable")}

	require.Empty(t, emaildelivery.LookupEmailRecipientLocale(t.Context(), store, "private-reader@example.test"))
	require.Empty(t, emaildelivery.LookupEmailIdentityLocale(t.Context(), store, "identity-1"))

	require.Contains(t, logs.String(), `"event":"mail.delivery.locale_lookup_failed"`)
	require.Contains(t, logs.String(), "database unavailable")
	require.NotContains(t, logs.String(), "private-reader@example.test")
}

func TestEmailRenderDataFallsBackToTheSiteSettings(t *testing.T) {
	logo := "https://cdn.example.test/logo.png"
	store := emaildeliverytest.NewStore()
	store.SeedRenderDefaults(emaildelivery.RenderDefaults{
		SiteTitle: "Example Studio", CompanyName: "Example Company", Logos: sitelogo.Email[string]{Light: &logo},
	})

	data := emaildelivery.BuildEmailRenderData(t.Context(), store, "https://cdn.example.test", "https://www.example.test/",
		"en", map[string]string{"site_origin": "https://input.example.test"})

	require.Equal(t, "Example Studio", data["site_name"])
	require.Equal(t, "https://www.example.test", data["site_origin"])
	require.Equal(t, "Example Company", data["site.companyName"])
	require.Equal(t, logo, data["logo_email_url"])
	require.Equal(t, logo, data["logo_email_dark_url"])
}

func TestEmailRenderDataWithoutSiteSettingsLogsOnlyAFailure(t *testing.T) {
	logs := captureLogs(t)
	store := emaildeliverytest.NewStore()

	data := emaildelivery.BuildEmailRenderData(t.Context(), store, "", "https://www.example.test", "en", nil)
	require.Empty(t, data["site_name"])
	require.Equal(t, "https://www.example.test", data["site.url"])
	require.Empty(t, logs.String(), "a database without the row renders without defaults")

	store.Fail = map[string]error{"EmailRenderDefaults": errors.New("database unavailable")}
	data = emaildelivery.BuildEmailRenderData(t.Context(), store, "", "https://www.example.test", "en", nil)
	require.Empty(t, data["site_name"])
	require.Contains(t, logs.String(), "Failed to load site settings for email defaults")
}

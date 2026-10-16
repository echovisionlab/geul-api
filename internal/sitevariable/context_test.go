package sitevariable

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestContextExactKeysAndSingleExpansion(t *testing.T) {
	c := FromSettings(model.SiteSettings{SiteTitle: "Raw & <sector>", CompanyName: "{{site.title}}", LegalEmail: "legal@example.com", ContactEmail: "contact@example.com", SupportEmail: "support@example.com", PrivacyEmail: "privacy@example.com"}, "https://www.rawsector.com/")
	require.Len(t, c.Values(), 7)
	require.Equal(t, "Raw & <sector> https://www.rawsector.com {{site.title}}", c.ResolveText("{{site.title}} {{site.url}} {{site.companyName}}"))
	_, err := c.Resolve("site.Title")
	require.Error(t, err)
	require.Equal(t, "", c.ResolveText(""))
}

func TestUnifiedContactAliasesPreserveExplicitClearAndLegalAddress(t *testing.T) {
	for _, address := range []string{"contact@example.com", ""} {
		settings := model.SiteSettings{ContactEmail: address, LegalEmail: "legal@example.com", SupportEmail: "old-support@example.com", PrivacyEmail: "old-privacy@example.com"}
		values := FromSettings(settings, "").Values()
		for _, key := range []string{"contact.email", "contact.supportEmail", "contact.privacyEmail"} {
			require.Equal(t, address, values[key], key)
		}
		require.Equal(t, settings.LegalEmail, values["contact.legalEmail"])
	}
	legacy := Context{SupportEmail: "support@example.com", PrivacyEmail: "privacy@example.com"}.Values()
	require.Equal(t, "support@example.com", legacy["contact.email"])
	require.Equal(t, "privacy@example.com", legacy["contact.privacyEmail"])
	require.Equal(t, "{{contact.email}}", TemplateContext().ResolveText("{{contact.supportEmail}}"))
}

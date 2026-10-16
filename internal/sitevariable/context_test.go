package sitevariable

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestContextExactKeysAndSingleExpansion(t *testing.T) {
	c := FromSettings(model.SiteSettings{SiteTitle: "Raw & <sector>", CompanyName: "{{site.title}}", LegalEmail: "legal@example.com", ContactEmail: "contact@example.com"}, "https://www.rawsector.com/")
	require.Len(t, c.Values(), 5)
	require.Equal(t, "Raw & <sector> https://www.rawsector.com {{site.title}}", c.ResolveText("{{site.title}} {{site.url}} {{site.companyName}}"))
	_, err := c.Resolve("site.Title")
	require.Error(t, err)
	require.Equal(t, "", c.ResolveText(""))
}

func TestContactPreservesExplicitClearAndLegalAddress(t *testing.T) {
	for _, address := range []string{"contact@example.com", ""} {
		settings := model.SiteSettings{ContactEmail: address, LegalEmail: "legal@example.com"}
		values := FromSettings(settings, "").Values()
		require.Equal(t, address, values["contact.email"])
		require.Equal(t, settings.LegalEmail, values["contact.legalEmail"])
	}
	require.Equal(t, "{{contact.email}}", TemplateContext().ResolveText("{{contact.email}}"))
}

func TestContextRejectsLegacyContactKeys(t *testing.T) {
	context := Context{ContactEmail: "contact@example.com"}
	for _, key := range []string{"contact.supportEmail", "contact.privacyEmail"} {
		_, err := context.Resolve(key)
		require.ErrorContains(t, err, "unknown site variable")
		require.NotContains(t, context.Values(), key)
		require.Equal(t, "{{"+key+"}}", context.ResolveText("{{"+key+"}}"))
	}
}

package sitevariable

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestContextExactKeysAndSingleExpansion(t *testing.T) {
	c := FromSettings(model.SiteSettings{SiteTitle: "Raw & <sector>", CompanyName: "{{site.title}}", LegalEmail: "legal@example.com", SupportEmail: "support@example.com", PrivacyEmail: "privacy@example.com"}, "https://www.rawsector.com/")
	require.Len(t, c.Values(), 6)
	require.Equal(t, "Raw & <sector> https://www.rawsector.com {{site.title}}", c.ResolveText("{{site.title}} {{site.url}} {{site.companyName}}"))
	_, err := c.Resolve("site.Title")
	require.Error(t, err)
	require.Equal(t, "", c.ResolveText(""))
}

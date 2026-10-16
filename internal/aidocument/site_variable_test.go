package aidocument

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSiteVariableCompactTuple(t *testing.T) {
	for _, source := range []string{`["siteVariable","site.title"]`, `["siteVariable","contact.email",true]`} {
		var item InlineItem
		require.NoError(t, json.Unmarshal([]byte(source), &item))
		encoded, err := json.Marshal(item)
		require.NoError(t, err)
		require.JSONEq(t, source, string(encoded))
	}
	for _, source := range []string{`["siteVariable","contact.supportEmail",true]`, `["siteVariable","contact.privacyEmail",true]`, `["siteVariable","unknown"]`, `["siteVariable","SITE.TITLE"]`, `["siteVariable","site.title",true]`, `["siteVariable","site.url",false]`, `["siteVariable","site.url",null]`, `["siteVariable","site.url",true,true]`} {
		var item InlineItem
		require.Error(t, json.Unmarshal([]byte(source), &item), source)
	}
}

package aidocument

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSiteVariableInlineItem(t *testing.T) {
	for _, source := range []string{`{"type":"site_variable","key":"site.title"}`, `{"type":"site_variable","key":"contact.email","linked":true}`} {
		var item InlineItem
		require.NoError(t, json.Unmarshal([]byte(source), &item))
		encoded, err := json.Marshal(item)
		require.NoError(t, err)
		require.JSONEq(t, source, string(encoded))
	}
	for _, source := range []string{
		`{"type":"site_variable","key":"contact.supportEmail","linked":true}`,
		`{"type":"site_variable","key":"contact.privacyEmail","linked":true}`,
		`{"type":"site_variable","key":"unknown"}`,
		`{"type":"site_variable","key":"SITE.TITLE"}`,
		`{"type":"site_variable","key":"site.title","linked":true}`,
		`{"type":"site_variable","key":"site.url","linked":false}`,
		`{"type":"site_variable","key":"site.url","linked":null,"text":"x"}`,
		`{"type":"site_variable"}`,
	} {
		var item InlineItem
		require.Error(t, json.Unmarshal([]byte(source), &item), source)
	}
}

package emailauthoring

import (
	"testing"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalEmailLayoutRoomLocaleRequiresExactCanonicalCode(t *testing.T) {
	t.Parallel()

	locale, err := canonicalEmailLayoutRoomLocale("zh-TW")
	require.NoError(t, err)
	assert.Equal(t, "zh-TW", locale)

	for _, value := range []string{"", " en ", "EN", "zh_tw", "xx"} {
		_, err := canonicalEmailLayoutRoomLocale(value)
		require.Error(t, err, value)
	}
}

func TestCompileEmailLayoutTargetValuesKeepsStableWrapper(t *testing.T) {
	t.Parallel()

	source, err := emailutil.CanonicalizeLayoutSourceMarkers(
		`<!doctype html><html><head><style>.content{color:red}</style></head><body><main data-shell="fixed"><h1>Source title</h1><p>Source body</p>{{content}}</main></body></html>`,
	)
	require.NoError(t, err)
	units, err := emailutil.ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 2)

	contentHTML, contentText, err := compileEmailLayoutTargetValues(source, []*intrav1.EmailLayoutLocaleValue{
		{Handle: units[0].Handle, Value: "번역 제목"},
		{Handle: units[1].Handle, Value: "번역 본문"},
	})
	require.NoError(t, err)
	values, err := emailutil.ExtractLayoutStoredLocaleValues(*contentHTML)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		units[0].Handle: "번역 제목",
		units[1].Handle: "번역 본문",
	}, values)
	assert.Contains(t, *contentText, "번역 제목")
	assert.Contains(t, *contentText, "번역 본문")

	rendered, err := emailutil.ResolveLayoutLocaleMarkup(source, contentHTML)
	require.NoError(t, err)
	assert.Contains(t, rendered, `<style>.content{color:red}</style>`)
	assert.Contains(t, rendered, `data-shell="fixed"`)
	assert.Contains(t, rendered, "<h1>번역 제목</h1>")
	assert.Contains(t, rendered, "<p>번역 본문</p>")
}

func TestCompileEmailLayoutTargetValuesPreservesExplicitEmptyAndSparseAbsence(t *testing.T) {
	t.Parallel()

	source, err := emailutil.CanonicalizeLayoutSourceMarkers(
		`<main><p>First</p><p>Second</p>{{content}}</main>`,
	)
	require.NoError(t, err)
	units, err := emailutil.ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 2)

	contentHTML, _, err := compileEmailLayoutTargetValues(source, []*intrav1.EmailLayoutLocaleValue{
		{Handle: units[0].Handle, Value: ""},
	})
	require.NoError(t, err)
	values, err := emailutil.ExtractLayoutStoredLocaleValues(*contentHTML)
	require.NoError(t, err)
	require.Contains(t, values, units[0].Handle)
	assert.Empty(t, values[units[0].Handle])
	assert.NotContains(t, values, units[1].Handle)
}

func TestCompileEmailLayoutTargetValuesRejectsUnknownAndDuplicateHandles(t *testing.T) {
	t.Parallel()

	source, err := emailutil.CanonicalizeLayoutSourceMarkers(
		`<main><p>Source body</p>{{content}}</main>`,
	)
	require.NoError(t, err)
	units, err := emailutil.ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 1)

	_, _, err = compileEmailLayoutTargetValues(source, []*intrav1.EmailLayoutLocaleValue{
		{Handle: "unknown", Value: "invalid"},
	})
	require.Error(t, err)

	_, _, err = compileEmailLayoutTargetValues(source, []*intrav1.EmailLayoutLocaleValue{
		{Handle: units[0].Handle, Value: "first"},
		{Handle: units[0].Handle, Value: "second"},
	})
	require.Error(t, err)
}

package translation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/net/html/atom"
)

func TestTranslationTextRules(t *testing.T) {
	t.Parallel()

	assert.True(t, IsPurePlaceholderText("{{content}}"))
	assert.False(t, IsPurePlaceholderText("Hello {{content}}"))
	assert.True(t, IsExcludedHTMLElement(atom.Style))
	assert.False(t, IsExcludedHTMLElement(atom.Main))
	assert.Equal(t, " \nTranslated\t", PreserveSourceEdgeWhitespace(" \nSource\t", " Translated "))
}

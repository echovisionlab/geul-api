package public

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLocalizedLabelName(t *testing.T) {
	t.Parallel()

	localized := "Localized label"
	explicitEmpty := ""
	assert.Equal(t, "Source label", localizedLabelName("Source label", nil))
	assert.Equal(t, localized, localizedLabelName("Source label", &localized))
	assert.Empty(t, localizedLabelName("Source label", &explicitEmpty))
}

func TestLabelHasEffectiveLogo(t *testing.T) {
	t.Parallel()

	light := " light-file "
	dark := "dark-file"
	blank := "   "

	assert.True(t, labelHasEffectiveLogo(&light, nil))
	assert.True(t, labelHasEffectiveLogo(nil, &dark))
	assert.False(t, labelHasEffectiveLogo(nil, nil))
	assert.False(t, labelHasEffectiveLogo(&blank, nil))
}

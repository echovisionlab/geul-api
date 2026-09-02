package form

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveFormTitleFallsBackForBlankSourceTitle(t *testing.T) {
	t.Parallel()

	blank := "  "
	assert.Equal(t, defaultFormTitle(), resolveFormTitle(nil))
	assert.Equal(t, defaultFormTitle(), resolveFormTitle(&blank))

	title := "Contact"
	assert.Equal(t, title, resolveFormTitle(&title))
}

package sliceutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUniqueNonZeroKeepsFirstOccurrenceOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"file-a", "file-b", "file-c"}, UniqueNonZero([]string{"", "file-a", "file-b", "file-a", "", "file-c", "file-b"}))
	assert.Equal(t, []string{" a", "a"}, UniqueNonZero([]string{" a", "a", " a"}))
	assert.Equal(t, []int{3, 1}, UniqueNonZero([]int{0, 3, 1, 3}))
	assert.Empty(t, UniqueNonZero[string](nil))
}

func TestUniqueTrimmedTrimsBeforeDeduplicating(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"one", "two"}, UniqueTrimmed([]string{" one ", "", "two", "one", "  "}))
	assert.Empty(t, UniqueTrimmed(nil))
}

func TestSortedUniqueTrimmedSortsDistinctValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b", "c"}, SortedUniqueTrimmed([]string{" c", "a", "", "b ", "a"}))
	assert.NotNil(t, SortedUniqueTrimmed(nil))
}

package seriestest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/series/seriestest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	t.Parallel()
	seriestest.RunStoreContract(t, seriestest.FakeFixture)
}

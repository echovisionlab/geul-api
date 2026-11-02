package accounttest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/account/accounttest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	t.Parallel()
	accounttest.RunStoreContract(t, accounttest.FakeFixture)
}

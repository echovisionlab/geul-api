package membertest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/member/membertest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	t.Parallel()
	membertest.RunStoreContract(t, membertest.FakeFixture)
}

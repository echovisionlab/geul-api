package criteriatest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/member/criteria/criteriatest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	criteriatest.RunStoreContract(t, func(*testing.T) criteriatest.Harness { return criteriatest.FakeHarness() })
}

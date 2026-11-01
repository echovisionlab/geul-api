package posttest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/post/posttest"
)

func TestFakeStoreKeepsThePostStoreContract(t *testing.T) {
	posttest.RunStoreContract(t, func(*testing.T) posttest.Harness { return posttest.FakeHarness() })
}

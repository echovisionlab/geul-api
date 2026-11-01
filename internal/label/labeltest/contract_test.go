package labeltest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/label/labeltest"
)

func TestFakeStoreKeepsTheLabelStoreContract(t *testing.T) {
	labeltest.RunStoreContract(t, func(*testing.T) labeltest.Harness { return labeltest.FakeHarness() })
}

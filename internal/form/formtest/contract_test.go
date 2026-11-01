package formtest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/form/formtest"
)

func TestFakeStoreKeepsTheFormStoreContract(t *testing.T) {
	formtest.RunStoreContract(t, func(*testing.T) formtest.Harness { return formtest.FakeHarness() })
}

func TestFakeStoreKeepsThePublicFormStoreContract(t *testing.T) {
	formtest.RunPublicStoreContract(t, func(*testing.T) formtest.PublicHarness { return formtest.FakePublicHarness() })
}

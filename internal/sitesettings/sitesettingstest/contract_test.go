package sitesettingstest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/sitesettings/sitesettingstest"
)

func TestFakeStoreKeepsTheSiteSettingsStoreContract(t *testing.T) {
	sitesettingstest.RunStoreContract(t, func(*testing.T) sitesettingstest.Harness { return sitesettingstest.FakeHarness() })
}

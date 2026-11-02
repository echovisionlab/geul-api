package mapthemetest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
)

func TestFakeStoreKeepsTheMapThemeStoreContract(t *testing.T) {
	mapthemetest.RunStoreContract(t, func(*testing.T) mapthemetest.Harness { return mapthemetest.FakeHarness() })
}

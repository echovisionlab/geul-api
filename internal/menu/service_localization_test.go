package menu

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMenuServiceValidateMenuItemsRejectsInvalidVisibilityRole(t *testing.T) {
	t.Parallel()

	url := "/authors"
	items := []Item{{
		ID:       "authors",
		Label:    "Authors",
		LinkType: LinkCustom,
		URL:      &url,
		Visibility: &Visibility{
			Mode:  VisibilityRoles,
			Roles: []string{"admin", "owner"},
		},
	}}

	err := validateMenuItems(items, 0)

	require.ErrorContains(t, err, "invalid role: owner")
}

package menu

import (
	"testing"

	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestMenuServiceValidateMenuItemsRejectsInvalidVisibilityRole(t *testing.T) {
	t.Parallel()

	url := "/authors"
	items := []*managev1.MenuItem{{
		Id:       "authors",
		Label:    "Authors",
		LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM,
		Url:      &url,
		Visibility: &managev1.MenuVisibility{
			Mode:  managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES,
			Roles: []string{"admin", "owner"},
		},
	}}

	err := (&MenuService{}).validateMenuItems(items, 0)

	require.ErrorContains(t, err, "invalid role: owner")
}

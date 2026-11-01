// Package ogprojection composes the OG projections of every entity family.
package ogprojection

import (
	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/og"
)

// AssetReferences are the owners of every row that can point at an OG
// asset, so cleanup keeps an asset any of them still uses. Legal routes keep
// no pointer row and are absent.
func AssetReferences() []og.AssetReference {
	return []og.AssetReference{
		postadapter.NewProjection(), pageadapter.NewProjection(), seriesadapter.NewProjection(),
		workadapter.NewProjection(), artistadapter.NewProjection(), labeladapter.NewProjection(),
		releaseadapter.NewProjection(), formogadapter.NewProjection(), sitesettingsadapter.NewProjection(),
	}
}

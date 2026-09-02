package sitesettings

import "github.com/echovisionlab/geul-api/internal/structured"

// Site-setting values are narrowed by their registered setting key before
// they can mutate the persisted singleton.
type siteSettingValue = structured.Value
type siteSettingObject = structured.Fields

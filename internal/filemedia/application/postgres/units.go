// Package applicationpostgres implements File deletion persistence.
package applicationpostgres

import "gorm.io/gorm"

type store struct{ tx *gorm.DB }

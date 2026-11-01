// Package filemediapostgres implements File persistence and owner adapters.
package filemediapostgres

import "gorm.io/gorm"

type store struct{ tx *gorm.DB }

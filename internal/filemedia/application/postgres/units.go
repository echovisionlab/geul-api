// Package applicationpostgres implements File deletion persistence.
package applicationpostgres

import (
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"gorm.io/gorm"
)

type store struct {
	tx         *gorm.DB
	references filemediapostgres.AttachmentReferences
}

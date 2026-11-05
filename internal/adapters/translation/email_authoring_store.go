package translationadapter

import (
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
)

// emailAuthoringPorts are the ports a translation step drives in the
// Email Authoring Store: the content store and Campaign's delivery history.
func emailAuthoringPorts(contents *contentblock.Store, references emailauthoringpostgres.DeliveryReferences) emailauthoringpostgres.Ports {
	return emailauthoringpostgres.Ports{ContentBlocks: contents, References: references}
}

// emailAuthoringStore binds the Email Authoring Store to the translation
// domain's database handle.
func emailAuthoringStore(db *gorm.DB, contents *contentblock.Store, references emailauthoringpostgres.DeliveryReferences) emailauthoring.Store {
	return emailauthoringpostgres.NewStore(db, emailAuthoringPorts(contents, references))
}

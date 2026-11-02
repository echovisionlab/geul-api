package programeventpostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// ProgramEventContentOwner supplies the already locked content owner's row query.
func ProgramEventContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("program_event AS owner").Where("owner.id = ?", id)
}

func ownerSignalPublisher(publisher contentsignal.Publisher) contentsignal.Publisher {
	return contentsignal.WithRevisionReader(publisher, contentsignal.RevisionReaderFunc(func(ctx context.Context, tx *gorm.DB, _ managev1.ContentEntityType, id string) (string, error) {
		return contentblock.ReadOwnerDocumentRevision(ProgramEventContentOwner(ctx, tx, id))
	}))
}

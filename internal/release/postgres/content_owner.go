package releasepostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// ReleaseContentOwner supplies the already locked content owner's row query.
func ReleaseContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("release AS owner").Where("owner.id = ?", id)
}

func ownerSignalPublisher(publisher contentsignal.Publisher) contentsignal.Publisher {
	return contentsignal.WithRevisionReader(publisher, contentsignal.RevisionReaderFunc(func(ctx context.Context, tx *gorm.DB, _ managev1.ContentEntityType, id string) (string, error) {
		return contentblock.ReadOwnerDocumentRevision(ReleaseContentOwner(ctx, tx, id))
	}))
}

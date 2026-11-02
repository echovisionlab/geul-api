package contentsignal

import (
	"context"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// RevisionReader resolves an owner's current document in the signal transaction.
type RevisionReader interface {
	DocumentRevision(context.Context, *gorm.DB, managev1.ContentEntityType, string) (string, error)
}
type revisionPublisher struct {
	Publisher
	RevisionReader
}

type RevisionReaderFunc func(context.Context, *gorm.DB, managev1.ContentEntityType, string) (string, error)

func (read RevisionReaderFunc) DocumentRevision(ctx context.Context, tx *gorm.DB, entity managev1.ContentEntityType, id string) (string, error) {
	return read(ctx, tx, entity, id)
}

func WithRevisionReader(publisher Publisher, reader RevisionReader) Publisher {
	if publisher == nil {
		return nil
	}
	if reader == nil {
		return publisher
	}
	return revisionPublisher{publisher, reader}
}

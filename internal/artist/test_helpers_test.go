//go:build integration

package artist

import (
	"context"
	"errors"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type recordingArtistFileDeleter struct {
	deletedIDs []string
}

func (d *recordingArtistFileDeleter) DeleteFileByID(_ context.Context, id string) error {
	d.deletedIDs = append(d.deletedIDs, id)
	return nil
}

func (d *recordingArtistFileDeleter) CleanupTrackUploadSessions(context.Context, string, string) error {
	return nil
}

func (d *recordingArtistFileDeleter) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func (d *recordingArtistFileDeleter) ReconcilePublishedEntityAssets(
	context.Context,
	managev1.TranscodeEntityType,
	string,
) error {
	return nil
}

type noopArtistAsyncPublisher struct{}

func (noopArtistAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopArtistAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (noopArtistAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	_ string,
	_ string,
	_ proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional executor is required")
	}
	return nil
}

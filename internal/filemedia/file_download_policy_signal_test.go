package filemedia

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type policySignalPublisher struct {
	events   []*managev1.ContentUpdatedEvent
	executor eventpkg.DBTX
	err      error
}

func (p *policySignalPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return errors.New("unexpected enqueue")
}
func (p *policySignalPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return errors.New("unexpected non-transactional signal")
}
func (p *policySignalPublisher) NotifyProtobufWithExecutor(_ context.Context, executor eventpkg.DBTX, signal string, message proto.Message) error {
	if signal != eventpkg.SignalContentUpdated {
		return errors.New("unexpected signal")
	}
	p.executor = executor
	p.events = append(p.events, proto.Clone(message).(*managev1.ContentUpdatedEvent))
	return p.err
}

func TestFileDownloadPolicyChangedEventOwners(t *testing.T) {
	for owner, kind := range map[string]managev1.ContentEntityType{
		"post":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		"page":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		"work":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		"program_event": managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT,
		"release":       managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE,
	} {
		t.Run(owner, func(t *testing.T) {
			event, err := fileDownloadPolicyChangedEvent(fileDownloadPolicyRelation{ResourceType: owner, ResourceID: "owner-id", Selector: fileDownloadPolicySelector{EntityID: "policy-target-id"}})
			require.NoError(t, err)
			require.Equal(t, kind, event.EntityType)
			require.Equal(t, "owner-id", event.EntityId)
			require.False(t, event.DocumentStateChanged)
			require.Nil(t, event.Locale)
			revision := uuid.NewString()
			event.DocumentRevision = &revision
			require.NoError(t, contentsignal.Validate(event))
			require.Equal(t, managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION, event.ChangedFields[0].Kind)
		})
	}
	_, err := fileDownloadPolicyChangedEvent(fileDownloadPolicyRelation{ResourceType: "track", ResourceID: "track-id"})
	require.Error(t, err)
}

func TestFileDownloadPolicySignalUsesOwningTransactionAndCurrentRevision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	revision := uuid.NewString()
	require.NoError(t, db.Exec(`CREATE TABLE content_document (id TEXT PRIMARY KEY, revision TEXT NOT NULL); CREATE TABLE release (id TEXT PRIMARY KEY, content_document_id TEXT); CREATE TABLE policy (audience TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_document VALUES ('doc', ?);`, revision).Error)
	require.NoError(t, db.Exec(`INSERT INTO release VALUES ('release', 'doc'); INSERT INTO policy VALUES ('disabled')`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE file (id TEXT PRIMARY KEY, delete_requested_at DATETIME);
		CREATE TABLE track (id TEXT PRIMARY KEY, release_id TEXT, audio_original_file_id TEXT, download_audience TEXT);
		INSERT INTO file VALUES ('audio', NULL);
		INSERT INTO track VALUES ('track', 'release', 'audio', 'disabled')`).Error)
	publisher := &policySignalPublisher{}
	service := &FileService{db: db, asyncPublisher: publisher}
	relation, err := service.resolveFileDownloadPolicyRelation(context.Background(), db, fileDownloadPolicySelector{
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, EntityID: "track",
	}, false)
	require.NoError(t, err)
	require.Equal(t, "release", relation.ResourceType)
	require.Equal(t, "release", relation.ResourceID)
	origin := uuid.NewString()
	ctx, err := contentsignal.WithClientOrigin(context.Background(), origin)
	require.NoError(t, err)
	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Table("policy").Where("1 = 1").Update("audience", "public").Error)
		return service.publishFileDownloadPolicyChanged(ctx, tx, relation)
	})
	require.NoError(t, err)
	require.Len(t, publisher.events, 1)
	require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE, publisher.events[0].EntityType)
	require.Equal(t, "release", publisher.events[0].EntityId)
	require.NotNil(t, publisher.executor)
	require.Equal(t, revision, publisher.events[0].GetDocumentRevision())
	require.Equal(t, origin, publisher.events[0].GetOrigin())
	require.NoError(t, contentsignal.Validate(publisher.events[0]))
	publisher.err = errors.New("signal failed")
	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Table("policy").Where("1 = 1").Update("audience", "restricted").Error)
		return service.publishFileDownloadPolicyChanged(ctx, tx, relation)
	})
	require.ErrorContains(t, err, "signal failed")
	var audience string
	require.NoError(t, db.Table("policy").Pluck("audience", &audience).Error)
	require.Equal(t, "public", audience)
	require.Error(t, service.publishFileDownloadPolicyChanged(ctx, db, relation))
	service.asyncPublisher = nil
	require.ErrorIs(t, service.publishFileDownloadPolicyChanged(ctx, db, relation), contentsignal.ErrPublisherRequired)
}

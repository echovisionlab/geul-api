package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediapolicy"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"
	"gorm.io/gorm"

	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// withTestMutations adds the runner used by the service constructor.
func withTestMutations(service *Service) *Service {
	if service.units == nil {
		return service
	}
	audit := service.auditWriter
	if audit == nil {
		audit = &audittest.Recorder{}
		service.auditWriter = audit
	}
	checker := service.spiceDB
	if checker == nil {
		checker = &auth.SpiceDBClient{}
	}
	if service.storeMutations == nil {
		runner, err := mutation.NewStoreRunner(service.units, checker, identitystate.PostgresFence{}, audit)
		if err != nil {
			panic(err)
		}
		service.storeMutations = runner
	}
	return service
}

// auditedUserContext is ctx as a request of user: the principal and the
// request context the domain audit attributes its records to.
func auditedUserContext(ctx context.Context, user *auth.UserInfo) context.Context {
	sessionID := user.SessionID.String()
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: user.IdentityID.String(), MemberID: user.MemberID.String(), SessionID: sessionID,
	})
	if err != nil {
		panic(err)
	}
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, request), user)
}

type nativeFilePorts struct {
	Transcoder                    any
	Posts, Pages, Works, Releases filemediapolicy.LockedPolicyAccess
	Events                        filemediapolicy.EventPolicyAccess
	Tracks                        filemediapostgres.UploadTracks
}

type nativeFileUnits struct {
	mutation.UnitOfWork[Store]
	db         *gorm.DB
	ports      filemediapostgres.Ports
	transcoder any
}

func withNativeFileUnits(service *Service, db *gorm.DB, owners nativeFilePorts) *Service {
	client, _ := service.spiceDB.(*auth.SpiceDBClient)
	ports := filemediapostgres.Ports{UploadWorks: filemediapolicy.WorkExistence{}, UploadTracks: owners.Tracks, PolicyOwners: filemediapolicy.NewPolicyOwners(filemediapolicy.PolicyOwnerPorts{Posts: owners.Posts, Pages: owners.Pages, Works: owners.Works, Events: owners.Events, Releases: owners.Releases, SpiceDB: client}), PolicyAudiences: filemediapolicy.Audiences{}}
	service.units = nativeFileUnits{UnitOfWork: service.units, db: db, ports: ports, transcoder: owners.Transcoder}
	ports = testFilePorts(service)
	if db != nil {
		service.units = nativeFileUnits{filemediapostgres.NewUnits(db, ports), db, ports, owners.Transcoder}
		service.ingestLocks = filemediapostgres.NewIngestLocks(db, ports)
		service.faviconCommits = filemediapostgres.NewFaviconCommits(db, ports)
	}
	return service
}
func testFileStore(s *Service, tx *gorm.DB) Store {
	return filemediapostgres.NewStore(tx, testFilePorts(s))
}
func testFilePorts(s *Service) filemediapostgres.Ports {
	var ports filemediapostgres.Ports
	var transcoder any
	if units, ok := s.units.(nativeFileUnits); ok {
		ports = units.ports
		transcoder = units.transcoder
	}
	ports.MeshJobs = filemediaqueue.Mesh(transcoder, s.asyncPublisher)
	ports.IngestTranscodes = filemediaqueue.Transcodes(transcoder, s.asyncPublisher)
	ports.DeletionQueue = filemediaqueue.Async(s.asyncPublisher)
	ports.PolicySignals = filemediaqueue.Policy(s.asyncPublisher)
	return ports
}

func freshFileIngestAuthorityForTest(ctx context.Context, tx *gorm.DB, service *Service, upload managev1.UploadType, entity managev1.TranscodeEntityType, id string) error {
	return requireFreshFileIngestAuthority(ctx, testFileStore(service, tx), service, upload, entity, id, func(user *auth.UserInfo) (bool, error) { return identitystate.LockActivePrincipal(ctx, tx, user) })
}

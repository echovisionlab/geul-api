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

// withTestMutations gives a Service built as a struct literal the mutation
// runner NewService builds from its database, SpiceDB client and audit
// appender. An unset appender records in memory.
func withTestMutations(service *Service) *Service {
	service = withTestStorage(service)
	if service.db == nil {
		return service
	}
	audit := service.auditWriter
	if audit == nil {
		audit = &audittest.Recorder{}
		service.auditWriter = audit
	}
	spiceDB := service.spiceDB
	if spiceDB == nil {
		spiceDB = &auth.SpiceDBClient{}
	}
	runner, err := mutation.NewRunner(service.db, spiceDB, identitystate.PostgresFence{}, audit)
	if err != nil {
		panic(err)
	}
	if service.mutations == nil {
		service.mutations = runner
	}

	if service.storeMutations == nil {
		service.storeMutations, err = mutation.NewStoreRunner(service.units, spiceDB, identitystate.PostgresFence{}, audit)
		if err != nil {
			panic(err)
		}
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

func withTestStorage(service *Service) *Service {
	if service.db != nil {
		service.ingestLocks = filemediapostgres.NewIngestLocks(service.db, testFilePorts(service))
		service.faviconCommits = filemediapostgres.NewFaviconCommits(service.db, filemediapostgres.Ports{})
		service.units = mutation.Postgres(service.db, func(tx *gorm.DB) Store {
			return testFileStore(service, tx)
		})
	}
	return service
}

func testFileStore(s *Service, tx *gorm.DB) Store {
	return filemediapostgres.NewStore(tx, testFilePorts(s))
}

func testFilePorts(s *Service) filemediapostgres.Ports {
	return filemediapostgres.Ports{UploadWorks: filemediapolicy.WorkExistence{}, MeshJobs: filemediaqueue.Mesh(s.publisher, s.asyncPublisher), IngestTranscodes: filemediaqueue.Transcodes(s.publisher, s.asyncPublisher), UploadTracks: s.trackAttachment, DeletionQueue: filemediaqueue.Async(s.asyncPublisher), PolicyOwners: filemediapolicy.NewPolicyOwners(filemediapolicy.PolicyOwnerPorts{Posts: s.postAccess, Pages: s.pagePolicyAccess, Works: s.workPolicyAccess, Events: s.programEventAttachment, Releases: s.releasePolicyAccess, SpiceDB: s.spiceDB}), PolicyAudiences: filemediapolicy.Audiences{}, PolicySignals: filemediaqueue.Policy(s.asyncPublisher)}
}

func freshFileIngestAuthorityForTest(ctx context.Context, tx *gorm.DB, service *Service, upload managev1.UploadType, entity managev1.TranscodeEntityType, id string) error {
	return requireFreshFileIngestAuthority(ctx, testFileStore(service, tx), service, upload, entity, id, func(user *auth.UserInfo) (bool, error) { return identitystate.LockActivePrincipal(ctx, tx, user) })
}

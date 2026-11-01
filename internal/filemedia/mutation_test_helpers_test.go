package filemedia

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// withTestMutations gives a Service built as a struct literal the mutation
// runner NewService builds from its database, SpiceDB client and audit
// appender. An unset appender records in memory.
func withTestMutations(service *Service) *Service {
	if service.mutations != nil || service.db == nil {
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
	service.mutations = runner
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

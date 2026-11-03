# Architecture

This document describes how a domain package is layered: transport adapters
call use cases, use cases depend on ports and the domain's own types, they run
mutations through one pipeline, and each table has one owning package.
`internal/member` is the reference domain for use cases and `Deps`; the
[ports checklist](#moving-a-domain-to-ports) is the reference for the
contract-first layering, and the [migration checklist](#migrating-a-domain)
lists the earlier use-case steps. `internal/architecture` enforces the rules
with ratchet tests.

## Layers

| Layer | Where | Does | Does not |
|---|---|---|---|
| Transport adapter | `connect_handler.go` in the domain package; MCP tools in `internal/adapters/mcp` | Translate a request into a command or query, call one use case, translate the result | Open transactions, check authorization, query tables, call another handler |
| Use case | Methods on the domain's `Service` | Validate input, authorize, run the domain rules, read and write the domain's own tables | Import `connectrpc.com/connect` or `internal/errors`, read HTTP headers |
| Domain types and ports | The domain package: plain structs, sentinel errors, the `Store` interface, other ports | Name the domain's values and invariants, and what the use cases need from outside | Carry gorm tags, generated API types or rows |
| Store adapter | `internal/<domain>/postgres` | Map domain types to rows, own the SQL, constraints and row locks | Authorize, audit, decide domain rules |
| Shared primitives | `internal/mutation`, `internal/domainerr`, `internal/identityread`, `internal/contentsignal`, `internal/identitystate`, `internal/authzmutation`, `internal/domainaudit` | Transactions, locks, fences, audit, outbox, error mapping | Know a domain |

## Ports and domain types

Most integration tests exist because a use case is written against gorm, a
row type and the SpiceDB client, so the only way to run it is with
PostgreSQL, SpiceDB and Kratos. A domain package that depends on ports runs
its use cases on fakes; only the adapter behind each port needs the real
system, and a contract test pins that adapter. The rules:

- **No infrastructure in the domain.** A domain package (every package under
  a root in `pipelineDomains`, except its `postgres` Store adapter) imports
  no `gorm.io/...`, no SQL driver (`lib/pq`, `pgx`), no `internal/model`, no
  `*auth.SpiceDBClient`, and names no `kratos.*` table. Generated API types
  (`geul-event-contracts/gen/...`) appear only in `connect_handler*.go`.
  Generated policy descriptors (`gen/api/policy/v1`: `policyv1.Post.Edit(id)`)
  are the authorization port's vocabulary and stay. Error payloads that
  `domainerr` defines with a generated enum (`CollaborationConflict`'s
  reason) are the one other exception.
- **Domain types.** The domain owns plain Go structs for what it stores and
  returns (`maptheme.Theme`, `maptheme.Snapshot`), with its invariants in
  constructors or validation, separate from `internal/model` rows and from
  proto messages. Use cases take commands and queries of plain values and
  return domain types. When a domain type would be a field-for-field copy of
  the row with no invariant, still write the plain struct and two explicit
  mapping functions in the Store adapter; do not reuse the row, add a generic
  mapper or map by reflection.
- **The Store port.** The domain declares `type Store interface` in its own
  package: one method per read or write the use cases make, named in the
  domain's language (`LockTheme`, `InsertTheme`, `UpdateTheme(id,
  expectedRevision, snapshot)`), domain types in and out, and exported
  sentinel errors for the outcomes the use cases branch on
  (`ErrThemeNotFound`, `ErrRevisionChanged`, `ErrDefaultTheme`) instead of
  `gorm.ErrRecordNotFound` or a `*pgconn.PgError`. A Store is bound to one
  transaction or read snapshot; it never opens one. A shared helper that
  takes `*gorm.DB` (`authorizationtarget.RequireLocked`, `identityread`
  fragments, `contentsignal.PublishInTransaction`) moves behind a Store
  method too (`LockContributor(ctx, memberID) (identityID, error)`), and the
  domain keeps classifying its answers (`authorizationtarget.FailedToDecide`).
- **Other ports.** Authorization is `mutation.Authorizer` (checks plus the
  relationship writer), or a domain interface embedding it with the extra
  SpiceDB calls the domain makes (`CheckActorCan`, `LookupResources`). The
  identity fence is `identitystate.Fence`. Audit is `domainaudit.Appender`.
  Another domain's table is reached through a port that domain owns; until
  it exists the Store adapter may touch it and the table-ownership baseline
  keeps the crossing.
- **The Store adapter.** `internal/<domain>/postgres` implements the Store
  over a gorm transaction and exports
  `NewUnits(db *gorm.DB) mutation.UnitOfWork[<domain>.Store]`. It keeps the
  exact SQL, `WithContext`, lock clauses and error texts the use case had,
  maps rows to domain types and constraint violations to the sentinels. A
  mapped violation keeps the driver error readable and in the chain:

  ```go
  type defaultThemeError struct{ error } // the *pgconn.PgError

  func (err defaultThemeError) Is(target error) bool { return target == maptheme.ErrDefaultTheme }
  func (err defaultThemeError) Unwrap() error        { return err.error }
  ```

- **Translators.** `connect_handler.go` maps proto requests to commands and
  domain results to proto messages; an MCP tool maps domain results to its
  own output structs. Keep the bytes identical: build timestamps with
  `timestamppb.New(t)` before the existing formatting, keep `nil` and empty
  slices as the row gave them (`slices.Clone`), and map float32 columns
  through the same `float64(float32(...))` the old code did.

## Use cases

A use case is an exported method on the domain's `Service`:

```go
func (s *Service) CreateMemberTag(ctx context.Context, command CreateMemberTagCommand) (Tag, error)
func (s *Service) ListMemberTags(ctx context.Context, query ListMemberTagsQuery) (TagPage, error)
func (s *Service) OwnSections(ctx context.Context) ([]managev1.MySection, error)
```

- **Input.** One struct per use case: `<Name>Command` for a mutation,
  `<Name>Query` for a read, even when it has one field. A use case without
  input takes only the context. Commands hold plain Go values. The only
  generated types a command or query may carry are the shared query language
  (`commonv1.FilterSpec`, `commonv1.SortSpec`), because `internal/query`
  consumes them.
- **Output.** A domain type (`member.Tag`, `member.TagPage`) or the generated
  read-model message that every adapter renders (`*managev1.AdminMember`,
  `*commonv1.MemberSummary`). Never a `connect.Response`. Return an RPC
  response message only when that message is the read model itself, as
  `UpdateMyPreferencesResponse.settings` embeds `GetMySettingsResponse`.
- **Errors.** Outcomes the use case decides are `domainerr` errors (see
  [Errors](#errors)). Errors from a shared helper pass through unchanged, even
  when the helper still returns a `*connect.Error`; the boundary keeps its
  code. Wrap an unexpected failure with `fmt.Errorf("...: %w", err)` and mark
  values a database error can echo with `failurecause.WithPrivate`.
- **Request-derived input.** The authenticated principal and the request
  correlation are in the context. The Connect authentication interceptor and
  the MCP gateway (`internal/adapters/mcp/context.go`) both put the principal
  there, so a use case reads it with
  `authorizationtarget.RequireAuthenticatedPrincipal(ctx)`. Anything the
  transport carries outside the message, such as the client address or
  `Accept-Language`, is read by the translator and passed as a field:
  `CurrentSessionQuery.ClientAddress` comes from `X-Forwarded-For` in
  `ConnectHandler.GetCurrentSession`. A use case never sees an `http.Header`.
- **Composition.** A use case may call another use case or a private helper
  (`UpdateMemberProfile` returns `s.adminMember(ctx, id)`). It never calls a
  Connect handler, and it never builds a `connect.Request`.

### Translators

A Connect method converts, calls, and converts back. `errs.Wrap` maps the
error; `respond` in `internal/member/connect_handler.go` is the shared shape:

```go
func (h *ConnectHandler) CreateMemberTag(ctx context.Context, req *connect.Request[managev1.CreateMemberTagRequest]) (*connect.Response[managev1.MemberTag], error) {
	tag, err := h.members.CreateMemberTag(ctx, CreateMemberTagCommand{Name: req.Msg.Name})
	return respond(memberTagMessage(tag), err)
}
```

An MCP tool depends on a narrow interface over the use cases and calls them
directly:

```go
type MemberTagReader interface {
	ListMemberTags(context.Context, member.ListMemberTagsQuery) (member.TagPage, error)
}
```

`expectedToolError` maps use-case errors through `errs.Classified`, the same
mapping the Connect boundary uses.

### Worked example: creating a Member tag

The command, the use case and its pipeline run, and the translator are the
whole feature:

```go
// CreateMemberTagCommand creates a Member tag.
type CreateMemberTagCommand struct {
	Name string
}

func (s *Service) CreateMemberTag(ctx context.Context, command CreateMemberTagCommand) (Tag, error) {
	return mutation.Run(ctx, s.mutations, mutation.Spec[Tag]{
		Access: freshAdmin(), // mutation.FreshAdminCan(policyv1.Platform.IsAdmin())
		Change: func(_ context.Context, tx *gorm.DB) (Tag, error) {
			name := strings.TrimSpace(command.Name)
			if name == "" {
				return Tag{}, domainerr.Required("name")
			}
			tag := model.UserTag{Name: name}
			if err := tx.Create(&tag).Error; err != nil {
				return Tag{}, err
			}
			return Tag{ID: tag.ID, Name: tag.Name, CreatedAt: tag.CreatedAt}, nil
		},
		Audit: func(tag Tag) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, tag.ID, tag.Name)
			})
		},
	})
}
```

Validation sits inside `Change` so a caller without administrator authority
gets "admin access required" before any validation message, as it did before
the migration. The Connect method is the translator shown above.

### Administrator checks

An administrator read calls `authorizationtarget.RequireGlobalAdmin` first. An
administrator mutation checks fresh authority inside its transaction with
`mutation.FreshAdminCan`, which also applies the identity fence: a deactivated
or banned administrator with a live session is rejected. When a mutation must
resolve its target before the transaction (to lock the target's Identity),
keep `RequireGlobalAdmin` as an early gate too, so a non-administrator learns
nothing about whether the target exists.

## The mutation pipeline

`internal/mutation` runs every mutation of a migrated domain:

```go
func NewStoreRunner[S any](units UnitOfWork[S], spiceDB Authorizer, fence identitystate.Fence, audit domainaudit.Appender) (*StoreRunner[S], error)
func Run[R, S any](ctx context.Context, runner *StoreRunner[S], spec StoreSpec[R, S]) (R, error)

type StoreSpec[R, S any] struct {
	Access        Access                                   // required
	Subjects      []string                                 // Identity IDs whose Member state changes
	Lock          func(context.Context, S) error           // domain root locks taken before Access
	Change        func(context.Context, S) (R, error)      // required
	Audit         func(R) Audit                            // required
	Outbox        func(context.Context, S, R) error
	Relationships func(R) (apply, compensate []policyv1.RelationshipMutation, err error)
}

// A domain that still works on *gorm.DB: S is the transaction itself.
type Runner = StoreRunner[*gorm.DB]
type Spec[R any] = StoreSpec[R, *gorm.DB]
func NewRunner(db *gorm.DB, spiceDB Authorizer, fence identitystate.Fence, audit domainaudit.Appender) (*Runner, error)

type Authorizer interface { // *auth.SpiceDBClient; authztest.Checker in tests
	authz.AuthorizationDecisionChecker
	authzmutation.RelationshipWriter
}

func FreshCan(can policyv1.Can, err error) Access      // identitystate.RequireFreshCan
func FreshAdminCan(can policyv1.Can, err error) Access // identitystate.RequireFreshAdminCan
func PrincipalFence(principal *auth.UserInfo) Access   // lock the principal's Identity only
func Locked[S any](check func(context.Context, S, Require) error) Access // FreshCan of an action the locked state picks
func Checked[S any](check func(context.Context, S) error) Access         // the domain's own check in the transaction

type Require func(can policyv1.Can) error
func IsDenied(err error) bool          // a fresh check's permission denial
func IsUnauthenticated(err error) bool // a missing or stale session
func IsNotFound(err error) bool        // a shared helper's not-found answer
func IsClassified(err error) bool      // any client answer, Connect code or use-case error

func RequestAudit(action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit
func MemberAudit(memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit
func VersionAudit(action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit
func AnonymousAudit(action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit // a public mutation without a session
func SystemAudit(service sharedtelemetry.ServiceName, action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit // a backend service, on no principal's behalf
func Audits(audits ...Audit) Audit
func Unchanged() Audit

func Read(ctx context.Context, db *gorm.DB, read func(*gorm.DB) error) error        // one read-only snapshot
func ReadLocked(ctx context.Context, db *gorm.DB, read func(*gorm.DB) error) error  // a snapshot that may lock rows
func ReadCurrent(ctx context.Context, db *gorm.DB, read func(*gorm.DB) error) error // read committed, locking what it reads
```

`FreshCan` and `FreshAdminCan` take a generated descriptor's two results
directly: `mutation.FreshCan(policyv1.Post.Edit(id))`.

`identitystate.Fence` is the Identity fence (`Lock`,
`LockActivePrincipal`, `WithMutation`); production passes
`identitystate.PostgresFence{}`. `Access` answers through the `Authorizer`
and the fence and nothing else.

### Units of work

The transaction comes from the domain's `UnitOfWork`, which binds the
domain's Store to each transaction it opens:

```go
type UnitOfWork[S any] interface {
	Transaction(ctx context.Context, lockAuthorization bool, work func(Tx[S]) error) error
	RelationshipTransaction(ctx context.Context, writer authzmutation.RelationshipWriter, work func(Tx[S], authzmutation.WriteRelationships) error) error
	Read(ctx context.Context, read func(S) error) error // mutation.Read: repeatable read, read only
}

func Postgres[S any](db *gorm.DB, bind func(tx *gorm.DB) S) UnitOfWork[S] // production
func mutationtest.New[S any](fake S) *mutationtest.UnitOfWork[S]           // in memory
```

`mutation.Postgres` opens exactly the transactions `Run` always opened: one
read-committed transaction, the global authorization lock first when
`Subjects` is set, and `authzmutation.Execute` when `Relationships` is set.
`Run` passes the Store to `Lock`, `Access` (`Locked`, `Checked`), `Change`
and `Outbox`; the fence and the audit appender still receive the gorm
transaction, which is nil in memory where their fakes ignore it. A use case
reads through `s.units.Read`. A domain that needs `ReadLocked` or
`ReadCurrent` adds that method to `UnitOfWork`, `Postgres` and
`mutationtest.UnitOfWork` with the same isolation, rather than opening a
transaction itself.

`mutationtest.UnitOfWork` serializes transactions like one connection and
restores failed transactions through `Savepoint() mutationtest.Checkpoint`.
Each fake selects its persisted `State` and `Restore` operation explicitly;
locks, failure injection and call observations stay outside the comparison.
`Read` compares independent snapshots, rejects persisted changes and restores
the checkpoint. `ReadLocked` and `ReadCurrent` allow successful writes and
restore on callback error or panic. State comparison cannot enforce SQL CHECK
constraints or statement-level read-only restrictions; actual PostgreSQL
contracts cover those rules.

The fake keeps `authzmutation.Execute`'s write-once contract and records the
steps `Run` took (`Steps()`: `StepAuthorizationLock`, `StepSubjects`,
`StepLock`, `StepAccess`, `StepChange`, `StepAudit`, `StepOutbox`,
`StepRelationships`, then `StepCommit` or `StepRollback`), so a use-case test
asserts order and rollback without PostgreSQL.

`Runner`, `Spec` and `NewRunner` are the store-less form of the same
pipeline for domains that have not moved: their closures receive the gorm
transaction (`S` is `*gorm.DB`), and every existing call compiles unchanged.
They go away when the last domain has moved.

`Run` fixes the order inside one transaction:

1. the global authorization lock, when `Relationships` or `Subjects` is set;
2. the `Subjects` Identity locks, sorted;
3. `Lock`: the domain root row locks;
4. `Access`: the caller's identity fence and fresh authorization;
5. `Change`;
6. the audit records `Audit` names;
7. `Outbox`;
8. the `Relationships` batch, written through `authzmutation.Execute` and
   compensated if the transaction rolls back.

Rules for a spec:

- **Access.** Use `FreshCan` with the domain's own action, or `FreshAdminCan`
  for an administrator-only mutation. `PrincipalFence` is for a principal's
  own lifecycle step that `Change` guards by state, such as onboarding, where
  the principal is not yet active.
- **Locked.** When the action depends on the locked target's state (Edit,
  or EditArchived while archived), `Locked` reads that state in `tx` and
  calls `require` with the action, which runs `FreshCan`'s check. A domain
  that answers a denial differently (a masked target answers not found)
  recognizes it with `IsDenied`, `IsUnauthenticated` or `IsNotFound`.
- **Checked.** An internal caller whose authority is not the request
  principal, such as a collaboration save attributed to its contributors,
  re-resolves that authority with `Checked` inside the transaction. A domain
  whose fresh check answers differently from `FreshCan` in more than the
  denial (its own unauthenticated, banned or not-found answers, a checker
  other than the SpiceDB client) runs that check through `Checked` too.
- **Subjects.** List the Identity of each Member whose state the mutation
  changes, resolved before `Run` (`authorizationtarget.RequireLinkedMember`).
  Re-read the target inside `Change`; `recheckTarget` in
  `internal/member/member_self_profile.go` shows the shape. Subjects take the
  global authorization lock first, which keeps two concurrent mutations that
  lock the same pair of Identities from deadlocking.
- **Lock.** A mutation whose root row serializes its writers locks it in
  `Lock`, so a request that waited on a concurrent writer authorizes as of
  when it proceeds, and a missing root answers NotFound before
  authorization. `Change` reads the locked row through the closure.
- **Audit.** Every mutation names a record or returns `Unchanged()` because
  nothing changed; `Audits` names several records of one change, in order,
  and skips the entries that are `Unchanged()`. A zero `Audit` fails the
  mutation. There is no optional writer.
- **Outbox.** Enqueue PGMQ commands and schedule `content.updated` in the
  transaction. Get the executor with `contentsignal.TransactionExecutor(tx)`,
  which refuses a pool that is not a live transaction; publish signals with
  `contentsignal.PublishInTransaction`.
- **Relationships.** When set, the batch must not be empty. Return the exact
  inverse as `compensate`.
- **Errors.** `Run` returns errors from the steps unchanged. Map
  `gorm.ErrRecordNotFound` and constraint violations after `Run`, as
  `profileEdit.failure` does.

Work that must happen after the commit, such as a metric or a confirmation
log, follows `Run` in the use case. A read that needs one snapshot of several
rows uses `mutation.Read` instead of opening a transaction. PostgreSQL
refuses row locks in a read-only transaction, so a projection that
share-locks its root uses `mutation.ReadLocked`, and a check that locks rows
`FOR UPDATE` to agree with the next mutation of them, such as whether upload
cleanup is still needed, or an authorized read that locks its target and the
principal's Identity read committed, uses `mutation.ReadCurrent`. Neither
writes.

`mutation.IsClassified` tells a domain that maps only unclassified failures,
such as content store sentinels, that a shared helper already answered.

## Errors

`internal/domainerr` holds the use-case error: a kind, the request field and a
client-safe message. Its cause stays server-side.

```go
type Error struct {
	Kind    Kind
	Field   string
	Message string
	Detail  proto.Message // machine-readable reason, such as a collaboration conflict
}
func (e *Error) WithCause(cause error) *Error

func New(kind Kind, field, message string) *Error
func InvalidField(field, reason string) *Error         // "field: reason"
func Required(field string) *Error                     // "field is required"
func ResourceNotFound(resource, id string) *Error      // "resource not found: id"
func Duplicate(resource, field, value string) *Error   // "resource with field 'value' already exists"
func Precondition(message string) *Error
func Denied(message string) *Error
func InvalidSession() *Error
func Retry(message string) *Error
func DependencyUnavailable(dependency string) *Error   // "dependency unavailable"
func AuthenticationRequired() *Error
func AccountBanned() *Error
func NoPermission(action, resource string) *Error     // "no permission to action this resource"
func AdminRequired() *Error
func AuthorRequired() *Error
func InvalidEntityType(entityType string) *Error
func InvalidEntityID() *Error

type CollaborationRevisions struct {
	Document     string
	Target       string
	TargetExists *bool
}
func CollaborationRevisionsOf(err error) CollaborationRevisions // the revisions a cause in err carries
func CollaborationConflict(reason intrav1.CollaborationConflictReason, message string, current CollaborationRevisions) *Error
func CollaborationRejection(reason intrav1.CollaborationMutationRejectionReason, message string) *Error
```

A collaboration save that lost a revision race answers `CollaborationConflict`
(failed precondition with a `CollaborationConflictDetail`); one the room may
not make answers `CollaborationRejection` (invalid argument with a
`CollaborationMutationRejectionDetail`). A conflict found while storing
reports the revisions its cause carries through `CollaborationRevisionsOf`
and keeps the cause in the chain when the domain branches on it.

Each constructor produces the same message as its `errs` counterpart, so a
use case can switch without changing a response;
`TestDomainErrorsMapToTheirConnectCounterparts` pins this. The mapping to
Connect lives in one place, `internal/errors/domain.go`:

| Kind | Connect code |
|---|---|
| `Invalid` | `InvalidArgument` |
| `NotFound` | `NotFound` |
| `AlreadyExists` | `AlreadyExists` |
| `FailedPrecondition` | `FailedPrecondition` |
| `PermissionDenied` | `PermissionDenied` |
| `Unauthenticated` | `Unauthenticated` |
| `Unavailable` | `Unavailable` |
| `Aborted` | `Aborted` |

`errs.Classified(err)` returns the Connect form of a Connect error or a
use-case error in the chain. `errs.Wrap`, `errs.BoundaryInterceptor`,
`errs.OperationLogLevel` and the MCP tool error mapping all use it. In tests
that call a use case directly, assert `errs.Wrap(err).Code()`.

## Composition

Each migrated domain has a `Deps` struct and one constructor:

```go
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Identity      auth.IdentityGetter
	AvatarFiles   UserAvatarAssetPromoter
	WelcomeEmails EmailCommandPublisher
	Accounts      AccountSummaryReader
	AccountEmails AccountEmailProjection
	Audit         AuditAppender
	CDNDomain     string // optional
	SiteOrigin    string // optional
}

func NewService(deps Deps) (*Service, error)
func NewConnectHandler(members *Service) *ConnectHandler
```

- `NewService` rejects a missing required collaborator and names it, so the
  server fails at startup. Mark an optional field with what its zero value
  means.
- A required collaborator is never an option function, and no method checks
  it for nil at request time. Type a port as the capability the use cases call
  (`UserAvatarAssetPromoter`) instead of asserting a wider interface inside a
  request.
- There is no constructor without audit. Tests pass
  `audittest.Recorder` (`internal/domainaudit/audittest`), or
  `audittest.Recorder{Err: ...}` to make every append fail.
- A domain on ports has no `DB` field:

  ```go
  type Deps struct {
      Units   mutation.UnitOfWork[Store] // <domain>postgres.NewUnits(db)
      SpiceDB Authorizer                 // mutation.Authorizer or the domain's wider interface
      Fence   identitystate.Fence        // identitystate.PostgresFence{}
      Audit   domainaudit.Appender
  }

  mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
  ```

  `cmd/server` passes `<domain>postgres.NewUnits(db)`, the SpiceDB client and
  `identitystate.PostgresFence{}`; all four are required.

### Test doubles

These packages import no container and run in `go test ./...`:

| Package | Stands in for | Use |
|---|---|---|
| `internal/domainaudit/audittest` | `domainaudit.Appender` | `Recorder{}` records; `Recorder{Err: e}` fails every append |
| `internal/authz/authztest` | `mutation.Authorizer`, `CheckActorCan` | `(&authztest.Checker{}).Allow(policyv1.Post.Edit(id))`, `authztest.AllowAll().Deny(...)`, `checker.For(identityID).Allow(...)`, `Fail(err)`, `FailWrites(err)`; inspect `Checks()`, `Applied()`, `Compensated()`, compare with `authztest.Of(identityID, can, nil)` |
| `internal/authz/authztest` | `*auth.SpiceDBClient` | `checker.Client(t)` serves the same rules over a loopback CheckPermission server, for code that still takes the client |
| `internal/identitystate/identitystatetest` | `identitystate.Fence` | `&identitystatetest.Fence{}` finds every principal active; `Deactivate(identityID)`, `Fence{Err: e}`; inspect `Locked()` |
| `internal/mutation/mutationtest` | `mutation.UnitOfWork` | `mutationtest.New[Store](fake)` over a fake with `Savepoint`; inspect `Steps()` |
| `internal/<domain>/<domain>test` | the domain's `Store` | the fake Store, its `New()` returning the fake and its unit of work, and the Store contract cases |
| `internal/account/accounttest` | the Kratos admin API (`account.Identities`, `auth.IdentityDeleter`) | `accounttest.NewKratos(store)`; `Put`, `Update`, `Fail(method, err)`, `FailIdentity(id, err)`, `OnCall(method, hook)`; inspect `Calls()`, `Writes()`, `Identity(id)`. A deleted Identity also leaves the fake store |

A use-case test on the fakes, in the shape `internal/maptheme` takes once it moves:

```go
store, units := mapthemetest.New()
checker := authztest.AllowAll()
service, err := maptheme.NewService(maptheme.Deps{
	Units: units, SpiceDB: checker, Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{Err: auditDown},
})
require.NoError(t, err)

_, err = service.UpdateMapThemeSnapshot(ctx, command)

require.ErrorIs(t, err, auditDown)
require.Equal(t, mutation.StepRollback, units.Steps()[len(units.Steps())-1])
stored, _ := store.Stored(themeID) // unchanged
```

The fakes know no schema and take no lock. Whether admin implies edit is the
test's rule; SpiceDB relationship semantics, lock order and the fence's SQL
stay in the few integration tests that need them.

### Store contracts

A Store's behavior is one set of cases run against both implementations
(`mutationtest.RunContract`): the fake in a unit test, the PostgreSQL store
in an integration test that needs only PostgreSQL
(`pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true})`,
no SpiceDB or Kratos, `t.Parallel`). The contract owns what only the
database can get wrong: SQL, constraints and their sentinel errors, revision
compare-and-set, rollback of a failed transaction, and row locks, checked
with two transactions (`mutationtest.RequireSecondWaits`, which waits for a
real lock waiter of the current database, never for a timeout). Use-case
tests own everything else. `internal/mutation/contract_example_test.go` and
`contract_example_integration_test.go` are the pattern in miniature:

```go
var notesContract = map[string]func(t *testing.T, h mutationtest.Harness[notes]){
	"a locked note makes a second writer wait for the first commit": func(t *testing.T, h mutationtest.Harness[notes]) { ... },
}

func TestFakeNotesKeepTheNotesContract(t *testing.T) { // unit
	mutationtest.RunContract(t, func(*testing.T) mutationtest.Harness[notes] {
		return mutationtest.FakeHarness[notes](&fakeNotes{revisions: map[string]int{}})
	}, notesContract)
}

func TestPostgresNotesKeepTheNotesContractIntegration(t *testing.T) { // integration, PostgreSQL only
	mutationtest.RunContract(t, func(t *testing.T) mutationtest.Harness[notes] {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return mutationtest.PostgresHarness(db, mutation.Postgres(db, func(tx *gorm.DB) notes { return postgresNotes{tx} }))
	}, notesContract)
}
```
- In the domain's tests, a `newTestService(t, Deps{...})` helper fills unset
  collaborators with stand-ins that fail when reached
  (`internal/member/service_test_helpers_test.go`).

## Stored statuses

A status stored in a table is a domain constant whose value is the string the
table already holds, and one function pair maps it to and from the API enum.
The database does not change.

```go
// internal/model/page.go
type FormStatus string

const (
	FormStatusDraft     FormStatus = "FORM_STATUS_DRAFT"
	FormStatusPublished FormStatus = "FORM_STATUS_PUBLISHED"
)

// internal/form/status.go
func formStatusToProto(status model.FormStatus) managev1.FormStatus
func formStatusFromProto(status managev1.FormStatus) (model.FormStatus, bool)
```

- The type and its constants live next to the model field, because
  `internal/model` cannot import the domain. `internal/model` never imports a
  generated package.
- The mapping lives in the owning domain. A second API surface (the public
  `openv1.FormStatus`) gets its own mapping in the package that serves it.
- Code compares and stores the constants. It never converts a generated enum
  with `.String()` or looks it up in `*_value`.
- A test ranges over the generated `*_name` map so a new API value fails until
  it is mapped (`internal/form/status_test.go`).
- A status derived in SQL rather than stored follows the same shape inside the
  domain: `member.AdminStatus` names the values of `memberAdminStatusSQL` and
  `AdminStatusFromProto` maps the Account summary status for MCP.

## Kratos identities

`internal/identityread` is the only package that names a Kratos table. It owns
the bilateral link (`identities.external_id = member.id`) and the meaning of
the ban columns:

```go
func JoinLinked(member, alias string) string     // JOIN of the Identity linked to the Member row aliased member
func LeftJoinLinked(member, alias string) string // the same, keeping Members without a live link
func BannedSQL(alias string) string              // administrator-facing ban state of the joined Identity
func ActiveSQL(alias string) string              // the joined Identity is active and not banned
func LockLinked(ctx context.Context, tx *gorm.DB, identityID, memberID string) error // share-lock the linked Identity
func SessionIdentitySQL(session, alias string) string // FROM a Kratos session joined to its Identity
func LiveSessionSQL(session string) string          // the session is active and unexpired
const ExistsSQL = `EXISTS (SELECT 1 FROM kratos.identities WHERE id = ?::uuid)`
func Exists(ctx context.Context, db *gorm.DB, identityID string) (bool, error)
func FirstSession(ctx context.Context, db *gorm.DB, sessionID string) (bool, error) // the session is its Identity's first
```

A query that needs another Kratos column or table gets a new fragment here
instead of naming the table. Writes go through the Kratos admin API.

## Ratchets

`internal/architecture` counts today's violations of these rules and fails when
a count grows or a new key appears. When a count drops, the test fails until
the baseline is lowered, so the baseline never hides a regression:

```sh
env -u API_TIMEOUT_MS GOWORK=off go test ./internal/architecture -run <Test> -update
```

`-update` only lowers a baseline. Baselines are `testdata/ratchet/*.txt`, one
`<count> <key>` line per key.

| Test | Baseline | Counts | Keys / total at the foundation |
|---|---|---|---|
| `TestTableOwnershipRatchet` | `table_ownership` | SQL text, `Table("x")` and `Model`/`Create`/`Delete(&model.X{})` touching a table another package owns (`tableOwners`), and any `kratos.*` table outside `identityread` | 305 package/table pairs, 573 references; 18 Kratos pairs in 14 packages |
| `TestMCPConnectRequestRatchet` | `mcp_connect_new_request` | `connect.NewRequest` per file in `internal/adapters/mcp` | 20 files, 152 calls |
| `TestPipelineDomainsOpenNoTransactionsRatchet` | `pipeline_domain_transactions` | `.Transaction(func`, `authzmutation.Execute`, `identitystate.WithMutation`, `authorizationtarget.WithMutation` in `pipelineDomains` | 4 files, 4 calls (member deletion and provisioning ports, `member/public` newsletter) |
| `TestPipelineDomainUseCasesAreTransportFreeRatchet` | `pipeline_domain_transport_imports` | `connect` or `internal/errors` imports outside `connect_handler*.go` in `pipelineDomains` | 5 imports (`member_deletion_avatar.go`, `member/public`) |
| `TestModelImportsNoGeneratedContractsRatchet` | `model_generated_imports` | generated contract imports in `internal/model` | 8 files |
| `TestGeneratedStatusNameConversionRatchet` | `generated_status_name_conversions` | `.String()` on a generated `*_STATUS_*` value and `*Status_value` lookups, per package | 33 packages, 391 conversions |
| `TestAuditWriterNilGuardRatchet` | `audit_writer_nil_guards` | `== nil` / `!= nil` on an audit writer, per package | 27 packages, 169 guards |
| `TestDomainPackagesDependOnPortsRatchet` | `domain_port_dependencies` | per domain package (Store adapters under `<domain>/postgres` excluded): files importing gorm, a SQL driver, `internal/model`, or generated contracts outside `connect_handler*.go` (policy descriptors allowed); references to `auth.SpiceDBClient` and `kratos.*` | 399 gorm, 343 model, 359 contracts, 12 driver files; 133 SpiceDB client and 2 Kratos references |

The table check resolves source-known variables, true type aliases and finite
registries within lexical scope. Mutable registry escapes and unproven targets
remain unknown; direct literal and model calls are collected independently of
inferred control flow. Owner query ports use the caller's transaction and
preserve the existing query count and snapshot.

`TestTableOwnersCoverEveryModelTable` fails when a model's table has no owner.
`pipelineDomains` in `use_case_ratchet_test.go` lists the migrated domains.

The domain port check allows policy descriptors, collaboration error reason enums,
and the common filter/sort types and their operator enums as port vocabulary.
`queue_messages.go`, `job_messages.go`, and `user_deletion_messages.go` are queue
transport boundaries. A business file that mixes vocabulary with other generated
types still counts as a dependency on generated contracts.

## Migrating a domain

Work in this order; each step keeps every Connect response and MCP tool output
identical, and the domain's integration tests are the check.

1. **Inventory.** List the domain's Connect methods, the MCP tools that call
   them (`grep -n <Method> internal/adapters/mcp`), its constructors and
   options, and its rows in the ratchet baselines.
2. **Deps.** Replace the constructors with `Deps` and
   `NewService(Deps) (*Service, error)`. Make every collaborator a request
   path uses required; delete nil checks, request-time "not configured"
   errors, option functions for required collaborators and the unaudited
   constructor. Update `cmd/server` and the tests (`newTestService`,
   `audittest.Recorder`).
3. **Use cases.** Move each Connect method's body to a `Service` method with a
   command or query struct. Replace `errs.*` constructors with `domainerr`, and
   `errs.Internal(err)` with the bare or `%w`-wrapped error. Replace every call
   to another handler with a use-case or helper call. Move header reads to the
   translator.
4. **Translators.** Add `ConnectHandler` in `connect_handler.go` with
   `NewConnectHandler`, one short method per RPC, and register it in
   `cmd/server`. Point the MCP tool interfaces at the use cases and drop their
   `connect.NewRequest` calls.
5. **Pipeline.** Run each mutation through `mutation.Run`. Choose `Access`
   from the authorization the handler had: `RequireFreshCan` becomes
   `FreshCan`, `RequireFreshAdminCan` becomes `FreshAdminCan`, and a fence-less
   `RequireGlobalAdmin` or `authz.RequireAdminCan` becomes `FreshAdminCan`. That
   last change adds the identity fence; add an integration test like
   `TestMemberAdminMutationsRejectStaleAdministratorIdentityIntegration` and
   call it out in the commit. Move `AppendOptionalRequest` and other audit
   calls into `Audit`, PGMQ and signal publishing into `Outbox`, and SpiceDB
   writes into `Relationships`.
6. **Statuses.** Add constants and the mapping pair for each stored status,
   with the coverage test, and replace the inline conversions.
7. **Kratos and foreign tables.** Replace `kratos.*` SQL with `identityread`
   fragments. For a table another domain owns, call a port the owner defines.
   Where that is out of reach, leave the baseline entry and say so in the
   report.
8. **Ratchets.** Add the domain to `pipelineDomains`, run
   `go test ./internal/architecture`, lower each dropped baseline with
   `-update`, and commit the baseline with the change. A grown count is a bug
   in the migration.
9. **Verify.** Build, vet with and without `-tags=integration`, unit tests,
   `golangci-lint`, and the domain's integration packages, plus the MCP and
   `cmd/server` packages when their wiring changed.

## Moving a domain to ports

A domain that already follows [Migrating a domain](#migrating-a-domain)
moves to [ports and domain types](#ports-and-domain-types) in this order.
Each step keeps every Connect response, MCP tool output
(`internal/adapters/mcp/testdata/tools.golden.json` and the tools' outputs),
DB value, lock order, authorization answer and audit record identical; the
domain's integration tests stay the check until step 9 replaces them.

1. **Inventory.** List the domain's rows in
   `internal/architecture/testdata/ratchet/domain_port_dependencies.txt` and
   `table_ownership.txt`, its `*gorm.DB` queries per use case
   (`grep -n "tx\.\|s\.db\|gorm\." internal/<domain>/*.go`), its SpiceDB calls
   (`grep -n "spiceDB\.\|SpiceDB" internal/<domain>/*.go`), the MCP tools that
   call its use cases, and every integration test with what it asserts.
2. **Domain types.** In the domain package, declare plain structs for what
   the use cases store and return, and sentinel errors for the outcomes they
   branch on. Use cases, commands and queries stop naming `model.*` and
   generated types; move proto mapping into `connect_handler.go` (and the
   internal handler) and point MCP tools at the domain types.
3. **Store port.** Declare `type Store interface` with one domain-named
   method per query or write the use cases make, bound to one transaction.
   Move shared `*gorm.DB` helpers behind Store methods. Declare the
   authorization port: `mutation.Authorizer`, or an interface embedding it
   with the domain's other SpiceDB calls.
4. **Use cases.** `Deps` becomes `Units`, `SpiceDB` (the port), `Fence`,
   `Audit` and the domain's other ports, with no `DB`. Build the runner with
   `mutation.NewStoreRunner`. Write each mutation as
   `mutation.Run(ctx, s.mutations, mutation.StoreSpec[R, Store]{...})` whose
   `Lock`, `Locked`, `Checked`, `Change` and `Outbox` call the Store; keep
   each step's order and each error mapping (`errors.Is(err, ErrXNotFound)`
   where the old code tested `gorm.ErrRecordNotFound`). Reads go through
   `s.units.Read`.
5. **PostgreSQL store.** In `internal/<domain>/postgres`, implement the Store
   over `*gorm.DB` with the old queries moved verbatim (same `WithContext`,
   lock clauses, `Returning`, error texts) and export
   `NewUnits(db, ports) mutation.UnitOfWork[<domain>.Store]` built with
   `mutation.Postgres` (`ports` only when the Store calls other domains in
   the transaction; see [recurring cases](#recurring-cases)). The directory
   is `postgres` and the package is `<domain>postgres`. Map rows to domain types with explicit functions and
   constraint violations to the sentinels (Is plus Unwrap, as above). Move
   SQL-only unit tests (constraint-code recognition) with it.
6. **Wiring.** `cmd/server` passes `<domain>postgres.NewUnits(db)`, the
   SpiceDB client and `identitystate.PostgresFence{}`. Update every other
   `NewService(Deps{...})` (other domains' tests, `cmd/backend-integration-stack`).
7. **Fake and contract.** In `internal/<domain>/<domain>test`, write the
   fake Store (a mutex, maps of domain values, `Savepoint` that clones them,
   the same sentinels and orderings as the SQL), `New()` returning the fake
   and `mutationtest.New[<domain>.Store](fake)`, seed helpers, and the
   contract cases; `RunStoreContract(t, open)` calls
   `mutationtest.RunContract`. Run it on `FakeHarness` in a unit test in
   `<domain>test` and on `PostgresHarness` in
   `internal/<domain>/postgres/store_contract_integration_test.go`. Add
   `./internal/<domain>/postgres` to the `db` band in
   `scripts/test/integration/catalog.go` (tell the harness owner). Cover each
   constraint the use cases map, each compare-and-set, each `FOR UPDATE`
   (`RequireSecondWaits`), and rollback.
8. **Use-case tests.** Write them in package `<domain>_test` (the fake
   package imports the domain, so an internal test cannot import it), with
   `<domain>test.New()`, `authztest.Checker`, `identitystatetest.Fence` and
   `audittest.Recorder`. Assert results, error codes through
   `errs.Wrap(err).Code()`, the store after the call, `Checks()`, `Applied()`,
   the audit records and, where order matters, `units.Steps()`. Keep the
   pure internal tests (validation, normalization) in package `<domain>`.
9. **Integration tests.** Classify each one by what it asserts:
   - use-case logic (validation, branching, no-op detection, error mapping,
     audit content, ordering, rollback of a failed step) becomes a use-case
     unit test and the integration test is deleted;
   - SQL, constraints, compare-and-set and row locks become Store contract
     cases;
   - genuinely end-to-end behavior stays, in package `<domain>_test`, built
     on `<domain>postgres.NewUnits`: SpiceDB relationship semantics and
     compensation, the identity fence with real Kratos rows, and authority
     rechecked after waiting on a real lock. Keep one of each kind, not one
     per use case; a duplicate of a unit or contract case is deleted.
   Report the integration test count and package wall time before and after.
10. **Ratchets.** Run `go test ./internal/architecture -run
    'TestDomainPackagesDependOnPortsRatchet|TestTableOwnershipRatchet' -update`
    and commit the lowered baselines. A foreign-table key that moved from
    `internal/<domain>` to `internal/<domain>/postgres` is a rename, not
    growth: edit that baseline line by hand in the same commit and say so.
    Any other new key is a bug in the move.
11. **Verify.** Build, vet with and without `-tags=integration`,
    `go test ./...`, `golangci-lint`, and the integration runs of
    `internal/<domain>`, `internal/<domain>/postgres` and every package whose
    wiring changed (`internal/adapters/mcp` and `cmd/server` unit tests at
    least).

### Recurring cases

The first six domains moved (Member criteria, then named audience; sharelink, menu, series, maptheme,
label with artist) met these cases. Follow the rule given here; where an
earlier domain used another name for the same thing, the final wave aligns
it.

- **Other domains in the transaction.** When the Store must call another
  domain inside its transaction (Site Settings slot clearing, the
  collaboration checkpoint, media, OG, Menu targets, Post membership),
  `<domain>postgres.NewUnits(db, Ports{...})` takes those adapters in one
  `Ports` struct, and `NewUnits` returns an error naming any nil field. The
  domain sees each call as a Store method in its own words
  (`RemoveMenuTargets`); when it makes several calls to one owner, a Store
  method may return a port interface the domain declares (`Posts()`).
- **Joining another pipeline's transaction.** A step that runs inside a
  transaction another domain opened (translation apply and interchange, the
  source-locale switch, an owner's target rewrite, an owner's cleanup
  adapter) binds the Store to that transaction:
  `<domain>postgres.Bind(tx, ports, audit) <domain>.Joined`, where
  `type Joined struct { Store Store; Audit domainaudit.Appender }` carries an
  appender bound to the same transaction. The domain function takes
  `Joined` and never sees `*gorm.DB`.
- **Which unit a read needs.** `units.Read` is a read-only repeatable-read
  snapshot, `units.ReadLocked` a repeatable-read snapshot that may lock, and
  `units.ReadCurrent` an authorized read-committed read that locks what it
  reads. A Store method that locks or writes (`PublicEvent` takes a share
  lock; `publiccontent.Resolve*` and `translation.LoadRuntimeSettings`
  insert default settings) says so in its comment and is never called under
  `units.Read`; the fake cannot catch this, so the contract case calls it
  through the PostgreSQL unit it needs. A read that must not hold locks goes
  through a port outside the snapshot.
- **Best-effort reads.** A lookup whose failure is logged and ignored must
  leave subsequent SQL usable. In a mutation, authorize first and isolate
  the lookup with `mutation.BestEffortRead` in the Store adapter. Independent
  display assets use a narrow pool port outside the settings snapshot; the
  port and its callees do not open a transaction for each asset. Settings and
  loader rows stay in one `units.Read` snapshot; optional assets may reflect
  readiness changes after that snapshot.
- **The fence in a domain check.** A `Checked` step of a Store domain reaches
  the identity fence through `mutation.Fenced`. Inside `Locked`,
  `errors.Is(err, identitystate.ErrInactivePrincipal)` tells an inactive
  principal from a denied action without a second fence lock.
- **Stored statuses on ports.** The domain declares its own status type whose
  values are the stored strings; the Store maps the `internal/model`
  constants to it. The `internal/model` constants stay for code that has not
  moved. The mapping to the API enum stays in `connect_handler.go`.
- **Generated vocabulary the ratchet still counts.** Besides policy
  descriptors and `domainerr` enum payloads, a domain may keep generated
  types that are the vocabulary of a port it calls: the `commonv1` query
  language (`FilterSpec`, `SortSpec`), the content store's rich-text and
  `contentblock` types, `managev1.AIDocumentFieldTarget`, collaboration
  conflict and rejection reasons (`intrav1`), the public localization
  fallback reason (`openv1`). Prefer a domain type for media and member
  references (`commonv1.AssetRef`, `MemberSummary`, media delivery refs):
  the final wave replaces the remaining ones, so a domain moved from now on
  declares its own. A value the domain branches on gets a domain type in
  any case. The `generated contracts` lines left in
  `domain_port_dependencies.txt` are these uses; the final wave exempts them
  in the ratchet.
- **Undecodable stored values.** When use cases answer a corrupt stored JSON
  value differently (skip, fail, require Manage), the domain type keeps it
  encoded and the domain decodes it.
- **Public packages.** A public package may take a narrower read-only port
  that `mutation.UnitOfWork[Store]` satisfies.
- **Contract fixtures.** Rows the Store does not own but needs (Members,
  files, content documents, a parent Label) are seeded through hooks on a
  domain harness that embeds `mutationtest.Harness[S]`
  (`SeedMember`, `SeedTarget`, ...); `RunStoreContract` then runs the cases
  with its own `t.Run` and `t.Parallel` loop.
- **Errors inside a contract case.** A failed statement aborts a PostgreSQL
  transaction, so a case that expects an error ends that transaction before
  it goes on, and the fake returns the same error rather than `false`.
- **Fake hooks and rollback.** A hook that plays a concurrent writer inside
  the fake's transaction is rolled back with it. Assert the retry sequence
  and the answer, not the rows that writer left.
- **Lock order counts implicit locks.** A foreign-key check takes KEY SHARE
  on the referenced row, and advisory locks (the participant parent graph)
  are locks too. Every path that takes an advisory lock takes it before any
  row lock on the same entities (`entityparticipant/hierarchy.go`).
- **Lock waiters on Kratos rows.** In a database cloned with Kratos,
  `kratos.*` are `postgres_fdw` foreign tables: a wait on an Identity row
  shows as `pg_stat_activity.wait_event = 'PostgresFdwGetResult'`, not in
  `pg_locks`. A lock-wait assertion on such a row counts both.
- **Shared runtimes behind Ports.** Runtimes that still take `*gorm.DB`
  (`contentblock`, other domains' adapters) are called only
  from the `postgres` package, so its `Ports` fields may name `*gorm.DB`.
  The domain sees a Store method.
- **Adapter tests.** A production adapter behind `Ports` (logo slots, image
  bindings, menu target rewrites) is checked on PostgreSQL alone in the
  `postgres` package, with authorization and the fence faked; it is neither
  a contract case nor an end-to-end test.
- **Query budgets.** A test that counts the SQL statements of a list or a
  read (N+1 guards) stays on PostgreSQL in the `postgres` package; a call
  count on the fake does not replace it.
- **Public stores.** A public package's Store gets a fake and a contract
  suite like the domain's when its methods allow; otherwise its SQL is
  covered by PostgreSQL-only tests in `<domain>/public/postgres`. The
  package is named `<domain>publicpostgres`.
- **Kratos rows in a contract.** A Store method that joins `kratos.*`
  (credited or contributing Members) opens the contract database with
  `BootstrapKratosStub: true`; it stays a PostgreSQL-only test.
- **Lock waits outside a contract.** Use `pgtest.RequireLockWaiters` /
  `RequireKratosLockWaiters`, which count waiters of the test's own
  database, including `PostgresFdwGetResult` waits; never a fixed sleep.
- **Runtime-only tests.** A test that needs the running API process (route
  registration of several domains) lives in a runtime test package such as
  `cmd/server/runtimetest`, not in a domain package.
- **Version restore.** Page, Work and Post call `Restore.Apply` in
  `versionrestore` through its content Store port and their metadata callbacks.
  Each domain adapter keeps the locked root fence and transaction. Production
  and fake stores use the same flow.
- **The catalog.** Add `./internal/<domain>/postgres` to the `db` band in
  `scripts/test/integration/catalog.go`, and remove a package whose last
  integration test is gone; the catalog drift test fails otherwise.
- **A mutation on no principal's behalf.** A system boundary that changes
  an Identity's own state with no request principal (the login role
  convergence a Kratos hook drives) still runs through `mutation.Run`:
  `PrincipalFence` of that Identity is its Access, and `SystemAudit` names
  its record. Run puts the audit before the relationship write, so a
  failed audit rolls back before SpiceDB is written. A no-op returns a
  sentinel from `Change`; Run hands it back after the rollback, and the use
  case matches it by identity so a joined rollback failure still surfaces
  (`authentication.SyncLoginRole`).
- **System access.** Scheduled work with no Identity subject uses
  `mutation.NewSystemStoreRunner` with `mutation.System` and `SystemAudit`
  (or `Unchanged`). The runner rejects Identity subjects, relationship
  batches and non-system audit records. The domain locks and checks its
  lifecycle state before changing it. Legal seals the effective notice run
  in `Outbox`, after its system audit; worker commits the OG target switch
  with the activation and dispatches the sealed notice after commit.
- **Immutable media lifecycle.** `mediaasset.Lifecycle` receives units of
  work over its Store. The owner's PostgreSQL adapter binds those units to
  its existing transaction when it allocates, completes or replaces an
  asset; the owner's mutation supplies authorization and audit. Cleanup
  receives a separate `CleanupStore`, an object store, a cache and owner
  ports, keeping physical deletion outside the prepare/finalize transactions.
- **Public download revalidation.** `filemedia/public.Service` evaluates
  downloads in one `ReadLocked` snapshot, then opens a second snapshot to
  revalidate and sign. Preserve the lock order: policy Member criteria, owner,
  draft principal, viewer, exact attachment relation. Sign while those locks
  remain held. Its Store embeds the media asset download port so every
  lookup joins the same pass. Delivery and download results use plain types;
  `connect_handler_media.go` translates the owner-facing protobuf surface,
  including unknown fields and partial in-place hydration on a signing error.
  `mediaauth.Reference` carries the exact expiry enforced by the signed token.
- **A Store shared by services in one domain.** When importing the service
  would create a cycle, put shared value types and Store contracts in a leaf
  package within that domain (`filemedia/domain`). The service may re-export
  those plain types; its SQL adapter imports the contracts. Persistence for
  a separate application stays beside it (`filemedia/application/postgres`).
- **Admission bookkeeping.** A write that no principal authorizes and no
  audit record describes, such as the auth code issuance reservation of an
  anonymous request, is not a domain mutation. It calls
  `units.Transaction(ctx, false, ...)`, the one read-committed transaction
  it always opened, or `units.Read` directly; its Store methods are
  contract cases like any other (`authentication.AuthCodeIssuanceLimiter`).
- **A principal-less flow across Kratos calls.** A system flow that holds
  an Identity's fence across Identity provider or SpiceDB calls (the Kratos
  settings and credential hooks, the email change reconciler, the account
  lifecycle reconcilers, the deletion worker, the timed-ban scheduler)
  cannot be one `mutation.Run`: each database write has to commit before
  the external call after it, so a later failure leaves it committed and a
  retry resumes there. Its `Units` add `Fenced(ctx, identityID, work)`,
  which holds the session form of the fence through
  `identitystate.WithMutationSteps`, and `Steps.Step`, one transaction per
  write in the order the flow commits them; `Lead` holds a reconciler's
  leader lock. The fake units record the fenced Identities and their step
  counts (`account.Units`, `accounttest.Units`).
- **Audit of a change Kratos committed.** A request mutation whose state
  change Kratos already committed under its own fence (an administrator's
  social login removal, a canonical email change) appends its record in
  one `units.Transaction` of its own after the Kratos call, and a hook that
  completes a Kratos settings change appends the record the hook built,
  with its audit ID, in the transaction that locks the pair. Neither runs
  through `mutation.Run`, whose Access would take a fence the original
  write did not take. A single system write with no audit record (the
  retention scheduler's enqueue, the scheduled deletion dispatch) is
  admission bookkeeping.
- **Per-request reads outside a snapshot.** A read made on every request
  of a hot path (the session principal Oathkeeper's gateway check needs)
  stays one statement behind a port outside the unit of work
  (`authentication.Sessions`), because a read-only snapshot adds a BEGIN
  and a COMMIT to every request.
- **HTTP components.** A domain's HTTP handlers that are not Connect RPCs
  (the Kratos public proxy, the unified login facade, the access recorder,
  the MCP gateway admission) keep their decisions in the handler, take
  their collaborators as ports (the reservation limiter, the Kratos
  identity finder, `Sessions`, the admission checker), and are tested with
  `httptest` on fakes. The command, use case and translator split applies
  to the Connect RPCs only.
- **Unexported seams in use-case tests.** A test in `<domain>_test` that
  needs an unexported clock or result field reaches it through
  `export_test.go`. An internal test cannot import `<domain>test` (it
  imports the domain), so it keeps a small local stub of the port.

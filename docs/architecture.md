# Architecture

This document describes how a domain package is layered: transport adapters
call use cases, use cases run mutations through one pipeline, and each table
has one owning package. `internal/member` is the reference domain; the
[migration checklist](#migrating-a-domain) at the end lists the steps for the
other domains. `internal/architecture` enforces the rules with ratchet tests.

## Layers

| Layer | Where | Does | Does not |
|---|---|---|---|
| Transport adapter | `connect_handler.go` in the domain package; MCP tools in `internal/adapters/mcp` | Translate a request into a command or query, call one use case, translate the result | Open transactions, check authorization, query tables, call another handler |
| Use case | Methods on the domain's `Service` | Validate input, authorize, run the domain rules, read and write the domain's own tables | Import `connectrpc.com/connect` or `internal/errors`, read HTTP headers |
| Shared primitives | `internal/mutation`, `internal/domainerr`, `internal/identityread`, `internal/contentsignal`, `internal/identitystate`, `internal/authzmutation`, `internal/domainaudit` | Transactions, locks, fences, audit, outbox, error mapping | Know a domain |

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
func NewRunner(db *gorm.DB, spiceDB *auth.SpiceDBClient, audit domainaudit.Appender) (*Runner, error)
func Run[R any](ctx context.Context, runner *Runner, spec Spec[R]) (R, error)

type Spec[R any] struct {
	Access        Access                                       // required
	Subjects      []string                                     // Identity IDs whose Member state changes
	Change        func(context.Context, *gorm.DB) (R, error)   // required
	Audit         func(R) Audit                                // required
	Outbox        func(context.Context, *gorm.DB, R) error
	Relationships func(R) (apply, compensate []policyv1.RelationshipMutation, err error)
}

func FreshCan(can policyv1.Can, err error) Access      // identitystate.RequireFreshCan
func FreshAdminCan(can policyv1.Can, err error) Access // identitystate.RequireFreshAdminCan
func PrincipalFence(principal *auth.UserInfo) Access   // lock the principal's Identity only

func RequestAudit(action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit
func MemberAudit(memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit
func Unchanged() Audit
```

`FreshCan` and `FreshAdminCan` take a generated descriptor's two results
directly: `mutation.FreshCan(policyv1.Post.Edit(id))`.

`Run` fixes the order inside one transaction:

1. the global authorization lock, when `Relationships` or `Subjects` is set;
2. the `Subjects` Identity locks, sorted;
3. `Access`: the caller's identity fence and fresh authorization;
4. `Change`;
5. the audit record `Audit` names;
6. `Outbox`;
7. the `Relationships` batch, written through `authzmutation.Execute` and
   compensated if the transaction rolls back.

Rules for a spec:

- **Access.** Use `FreshCan` with the domain's own action, or `FreshAdminCan`
  for an administrator-only mutation. `PrincipalFence` is for a principal's
  own lifecycle step that `Change` guards by state, such as onboarding, where
  the principal is not yet active.
- **Subjects.** List the Identity of each Member whose state the mutation
  changes, resolved before `Run` (`authorizationtarget.RequireLinkedMember`).
  Re-read the target inside `Change`; `recheckTarget` in
  `internal/member/member_self_profile.go` shows the shape. Subjects take the
  global authorization lock first, which keeps two concurrent mutations that
  lock the same pair of Identities from deadlocking.
- **Audit.** Every mutation names a record or returns `Unchanged()` because
  nothing changed. A zero `Audit` fails the mutation. There is no optional
  writer.
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
log, follows `Run` in the use case.

## Errors

`internal/domainerr` holds the use-case error: a kind, the request field and a
client-safe message. Its cause stays server-side.

```go
type Error struct {
	Kind    Kind
	Field   string
	Message string
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
func DependencyUnavailable(dependency string) *Error   // "dependency unavailable"
```

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
const ExistsSQL = `EXISTS (SELECT 1 FROM kratos.identities WHERE id = ?::uuid)`
func Exists(ctx context.Context, db *gorm.DB, identityID string) (bool, error)
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

`TestTableOwnersCoverEveryModelTable` fails when a model's table has no owner.
`pipelineDomains` in `use_case_ratchet_test.go` lists the migrated domains.

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

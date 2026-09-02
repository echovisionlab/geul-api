package account

import (
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"gorm.io/gorm"
)

const (
	accountLifecycleStateConfirmationPending         = "confirmation_pending"
	accountLifecycleStateScheduled                   = "scheduled"
	accountLifecycleStateRecoveryConfirmationPending = "recovery_confirmation_pending"
	accountLifecycleStateCancelled                   = "cancelled"
	accountLifecycleStateRecovered                   = "recovered"
	accountLifecycleReconcileInterval                = 2 * time.Second
	accountLifecycleReconcileBatchSize               = 50
	accountLifecycleLeaderLockKey                    = int64(0x4745554c41434354) // GEULACCT
)

var accountLifecycleDeletionPendingStates = []string{
	accountLifecycleStateScheduled,
	accountLifecycleStateRecoveryConfirmationPending,
}

type AccountLifecycleService struct {
	db             *gorm.DB
	kratosClient   auth.IdentityManager
	spicedb        *auth.SpiceDBClient
	publisher      EmailCommandPublisher
	baseURL        string
	interval       time.Duration
	auditWriter    domainaudit.Appender
	memberDeletion MemberDeletionLifecycle
	memberEmails   MemberEmailProjection
}

type AccountLifecycleOption func(*AccountLifecycleService)

// WithLifecycleMemberDeletion supplies the Member-owned state projection and
// terminal cleanup required by the Account lifecycle service.
func WithLifecycleMemberDeletion(lifecycle MemberDeletionLifecycle) AccountLifecycleOption {
	return func(service *AccountLifecycleService) { service.memberDeletion = lifecycle }
}

// WithLifecycleMemberEmailProjection supplies the Member projection required
// to resolve lifecycle email recipients.
func WithLifecycleMemberEmailProjection(projection MemberEmailProjection) AccountLifecycleOption {
	return func(service *AccountLifecycleService) { service.memberEmails = projection }
}

func NewAuditedAccountLifecycleService(
	db *gorm.DB,
	kratosClient auth.IdentityManager,
	spicedb *auth.SpiceDBClient,
	baseURL string,
	publisher EmailCommandPublisher,
	auditWriter domainaudit.Appender,
	options ...AccountLifecycleOption,
) *AccountLifecycleService {
	if auditWriter == nil {
		panic("account lifecycle audit writer is required")
	}
	service := newAccountLifecycleService(db, kratosClient, spicedb, baseURL, publisher, options...)
	service.auditWriter = auditWriter
	return service
}

type AccountDeletionRequestResult struct {
	AlreadyScheduled bool
}

type AccountDeletionScheduledResult struct {
	ScheduledAt time.Time
}

func NewAccountLifecycleService(
	db *gorm.DB,
	kratosClient auth.IdentityManager,
	spicedb *auth.SpiceDBClient,
	baseURL string,
	publisher EmailCommandPublisher,
	options ...AccountLifecycleOption,
) *AccountLifecycleService {
	return newAccountLifecycleService(db, kratosClient, spicedb, baseURL, publisher, options...)
}

func newAccountLifecycleService(
	db *gorm.DB,
	kratosClient auth.IdentityManager,
	spicedb *auth.SpiceDBClient,
	baseURL string,
	publisher EmailCommandPublisher,
	options ...AccountLifecycleOption,
) *AccountLifecycleService {
	if db == nil {
		panic("db is required")
	}
	if kratosClient == nil {
		panic("kratosClient is required")
	}
	if spicedb == nil {
		panic("spicedb is required")
	}
	if publisher == nil {
		panic("publisher is required")
	}
	service := &AccountLifecycleService{
		db:           db,
		kratosClient: kratosClient,
		spicedb:      spicedb,
		publisher:    publisher,
		baseURL:      strings.TrimRight(baseURL, "/"),
		interval:     accountLifecycleReconcileInterval,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if service.memberDeletion == nil {
		panic("account lifecycle member deletion is required")
	}
	if service.memberEmails == nil {
		panic("account lifecycle member email projection is required")
	}
	return service
}

package legal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Deps are the collaborators of the Terms and Privacy management use cases.
// Every field except BaseURL is required; NewService rejects a missing one.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	OG            OG
	Notice        NoticeDelivery
	Audit         domainaudit.Appender
	// BaseURL is the site origin of notice links; empty keeps them relative.
	BaseURL string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil,
		"OG": d.OG == nil, "Notice": d.Notice == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("legal dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Terms and Privacy management use cases. Each takes the
// PolicyKind it acts on.
type Service struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	baseURL       string
	notice        NoticeDelivery
	auditWriter   domainaudit.Appender
	contentBlocks *contentblock.Store
	legalOG       OG
}

// NewService validates deps and returns the legal policy use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		db:            deps.DB,
		spiceDB:       deps.SpiceDB,
		baseURL:       deps.BaseURL,
		notice:        deps.Notice,
		auditWriter:   deps.Audit,
		contentBlocks: deps.ContentBlocks,
		legalOG:       deps.OG,
	}, nil
}

// Policy is one Terms or Privacy version with its typed Block document.
type Policy struct {
	ID             string
	Version        int32
	Title          string
	Document       *contentv1.RichTextDocument
	Revision       string
	SourceLocale   string
	SnapshotDigest string
	Status         model.LegalPolicyStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
	EffectiveFrom  *time.Time
	EffectiveUntil *time.Time
}

// PolicyLifecycle is the lifecycle state a schedule, cancel or activation
// left.
type PolicyLifecycle struct {
	ID               string
	Changed          bool
	DocumentRevision string
	Status           model.LegalPolicyStatus
	UpdatedAt        time.Time
	EffectiveFrom    *time.Time
	EffectiveUntil   *time.Time
}

// policyRow is a row of terms_history or privacy_history; the kinds share
// the columns.
type policyRow struct {
	ID                string     `gorm:"column:id;primaryKey"`
	Version           int        `gorm:"column:version"`
	Title             string     `gorm:"column:title"`
	Status            string     `gorm:"column:status"`
	ContentDocumentID *string    `gorm:"column:content_document_id"`
	EffectiveFrom     *time.Time `gorm:"column:effective_from"`
	EffectiveUntil    *time.Time `gorm:"column:effective_until"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (row policyRow) lifecycle(documentRevision string, changed bool) PolicyLifecycle {
	return PolicyLifecycle{
		ID: row.ID, Changed: changed, DocumentRevision: documentRevision,
		Status: model.LegalPolicyStatus(row.Status), UpdatedAt: row.UpdatedAt,
		EffectiveFrom: row.EffectiveFrom, EffectiveUntil: row.EffectiveUntil,
	}
}

// lockPolicyRow locks the version for update. A missing version answers
// NotFound in the kind's wording.
func lockPolicyRow(ctx context.Context, tx *gorm.DB, kind *policyKind, id string) (policyRow, error) {
	var row policyRow
	err := tx.WithContext(ctx).Table(kind.table).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, domainerr.ResourceNotFound(kind.notFoundResource, id)
	}
	return row, err
}

// GetPolicyVersionQuery reads one version.
type GetPolicyVersionQuery struct {
	ID string
}

// GetPolicyVersion returns one history row with its typed Block document. A
// version the caller may not view answers NotFound.
func (s *Service) GetPolicyVersion(ctx context.Context, kind PolicyKind, query GetPolicyVersionQuery) (Policy, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row policyRow
		if err := tx.Table(descriptor.table).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).First(&row, "id = ?", query.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.ResourceNotFound(descriptor.notFoundResource, query.ID)
			}
			return err
		}
		if err := requireLegalVersionViewOrNotFound(ctx, tx, s.spiceDB, descriptor.entity(), row.ID, row.Status); err != nil {
			return err
		}
		policy, err = s.policyWithDocument(ctx, tx, descriptor, row.ID)
		return err
	})
	return policy, err
}

// ListPolicyVersionsQuery pages through versions, newest first.
type ListPolicyVersionsQuery struct {
	// Status keeps only versions in that state when set.
	Status *model.LegalPolicyStatus
	// Limit defaults to 20.
	Limit  int32
	Offset int32
}

// PolicyPage is one page of versions.
type PolicyPage struct {
	Versions   []Policy
	Pagination *commonv1.PaginationResponse
}

// ListPolicyVersions returns one page of versions with their documents.
func (s *Service) ListPolicyVersions(ctx context.Context, kind PolicyKind, query ListPolicyVersionsQuery) (PolicyPage, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return PolicyPage{}, err
	}
	can, err := descriptor.list()
	if err != nil {
		return PolicyPage{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return PolicyPage{}, err
	}
	list := s.db.WithContext(ctx).Table(descriptor.table)
	if query.Status != nil {
		list = list.Where("status = ?", *query.Status)
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return PolicyPage{}, err
	}
	limit := int32(20)
	if query.Limit > 0 {
		limit = query.Limit
	}
	var rows []policyRow
	if err := list.Order("version DESC").Limit(int(limit)).Offset(int(query.Offset)).Find(&rows).Error; err != nil {
		return PolicyPage{}, err
	}
	versions := make([]Policy, len(rows))
	for i, row := range rows {
		if versions[i], err = s.policyWithDocumentInTransaction(ctx, descriptor, row.ID); err != nil {
			return PolicyPage{}, err
		}
	}
	return PolicyPage{
		Versions: versions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  query.Offset,
			HasMore: query.Offset+limit < int32(total),
		},
	}, nil
}

// CreatePolicyVersionCommand creates a draft version. An empty Title takes
// the kind's default title; AcceptLanguage selects the source locale.
type CreatePolicyVersionCommand struct {
	Title          *string
	Document       *contentv1.RichTextDocument
	AcceptLanguage string
}

// CreatePolicyVersion creates the next draft version with its content
// document and grants its policy relationship.
func (s *Service) CreatePolicyVersion(ctx context.Context, kind PolicyKind, command CreatePolicyVersionCommand) (Policy, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return Policy{}, err
	}
	can, err := descriptor.create()
	if err != nil {
		return Policy{}, err
	}
	var row policyRow
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		// Serialize version assignment.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", descriptor.table+"_version").Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if row, err = s.createDraftVersion(ctx, tx, descriptor, command); err != nil {
			return err
		}
		if err := appendLegalPolicyIdentityAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLegalPolicyCreated, descriptor.entity(), row.ID, row.Version); err != nil {
			return err
		}
		touchPolicy, err := descriptor.touchPolicy(row.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := descriptor.deletePolicy(row.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy})
	})
	if err != nil {
		return Policy{}, err
	}
	return s.policyWithDocumentInTransaction(ctx, descriptor, row.ID)
}

func (s *Service) createDraftVersion(ctx context.Context, tx *gorm.DB, kind *policyKind, command CreatePolicyVersionCommand) (policyRow, error) {
	var row policyRow
	title := kind.defaultTitle
	if command.Title != nil && *command.Title != "" {
		title = *command.Title
	}
	now := dbtime.Now()
	sourceLocale := resolveInitialSourceLocale(ctx, tx, command.AcceptLanguage)
	created, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile: legalContentDocumentProfile, SourceLocale: sourceLocale,
	})
	if err != nil {
		return row, normalizeLegalContentBlockError(kind.entity(), err)
	}
	// The version follows both the history and the notices sealed for
	// deleted versions, so a version number is never reused.
	if err := tx.Raw(`
		INSERT INTO `+kind.table+` (title, content, status, version, created_at, updated_at, content_document_id, source_locale)
		VALUES (?, '', ?, (SELECT GREATEST(
			COALESCE((SELECT MAX(version) FROM `+kind.table+`), 0),
			COALESCE((SELECT MAX(`+kind.sourceVersionColumn+`) FROM email_delivery_run WHERE `+kind.referenceColumn+` IS NOT NULL), 0)
		) + 1), ?, ?, ?, ?)
		RETURNING id, version, title, content, status, created_at, updated_at, content_document_id
	`, title, kind.draft, now, now, created.Document.ID.String(), sourceLocale).Scan(&row).Error; err != nil {
		return row, err
	}
	if command.Document != nil {
		if command.Document.GetSourceLocale() != sourceLocale {
			return row, domainerr.InvalidField("document.source_locale", "must match the server-selected source locale")
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, command.Document)
		if err != nil {
			return row, normalizeLegalContentBlockError(kind.entity(), err)
		}
		if _, err := s.contentBlocks.ReplaceSnapshot(ctx, tx, replacement, legalDocumentOwnershipFence(kind.entity(), row.ID)); err != nil {
			return row, normalizeLegalContentBlockError(kind.entity(), err)
		}
	}
	finalSnapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, created.Document.ID, sourceLocale)
	if err != nil {
		return row, normalizeLegalContentBlockError(kind.entity(), err)
	}
	return row, (internalLegalDocumentService{db: tx, kind: kind.entity(), contentBlocks: s.contentBlocks, legalOG: s.legalOG}).
		refreshDerivedContentProjectionsWithDB(ctx, tx, row.ID, finalSnapshot, sourceLocale, now)
}

// SchedulePolicyCommand schedules a draft version to take effect at
// EffectiveFrom, which must be in the future.
type SchedulePolicyCommand struct {
	ID               string
	ExpectedRevision string
	EffectiveFrom    *time.Time
}

// SchedulePolicy schedules the version and seals its update notice, which
// dispatches after the commit.
func (s *Service) SchedulePolicy(ctx context.Context, kind PolicyKind, command SchedulePolicyCommand) (PolicyLifecycle, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return PolicyLifecycle{}, err
	}
	if command.EffectiveFrom == nil {
		return PolicyLifecycle{}, domainerr.Required("effective_from")
	}
	effectiveFrom := command.EffectiveFrom.UTC().Truncate(dbtime.Precision)
	now := time.Now().UTC()
	if effectiveFrom.Before(now) {
		return PolicyLifecycle{}, domainerr.InvalidField("effective_from", "must be in the future")
	}
	previewURL, err := newAutomaticLegalNoticePreviewURL(s.baseURL)
	if err != nil {
		return PolicyLifecycle{}, err
	}
	var row policyRow
	var run *model.CampaignDeliveryRun
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if row, err = s.lockForLifecycle(ctx, tx, descriptor, command.ID, &command.ExpectedRevision); err != nil {
			return err
		}
		if row.Status != string(descriptor.draft) {
			return domainerr.Precondition(descriptor.scheduleNonDraft)
		}
		if err := EnsureLegalNoticeScheduleSlotWithDB(ctx, tx, descriptor.entity(), row.ID); err != nil {
			return err
		}
		mutationNow := dbtime.Now()
		if err := tx.Table(descriptor.table).Where("id = ?", row.ID).Updates(structured.Fields{
			"status": descriptor.scheduled, "effective_from": effectiveFrom, "updated_at": mutationNow,
		}).Error; err != nil {
			return err
		}
		row.Status, row.EffectiveFrom, row.UpdatedAt = string(descriptor.scheduled), &effectiveFrom, mutationNow
		if err := s.legalOG.RequestSaved(ctx, tx, descriptor.entity(), command.ID, "", true, descriptor.entity()+"_scheduled"); err != nil {
			return err
		}
		run, err = s.notice.CreateRun(
			ctx, tx, descriptor.entity(), row.ID, descriptor.updateEvent.String(),
			map[string]string{
				"policy_title":   row.Title,
				"effective_date": effectiveFrom.Format("2006-01-02"),
				"preview_url":    previewURL,
			},
			legalNoticeDispatchAt(now, effectiveFrom),
		)
		if err != nil {
			return err
		}
		return appendLegalPolicyLifecycleAudit(
			ctx, tx, s.auditWriter, descriptor.entity(), row.ID, row.Version,
			[]string{"effective_at", "status"}, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStateScheduled,
			&effectiveFrom, nil,
		)
	}); err != nil {
		return PolicyLifecycle{}, err
	}
	dispatchCreatedLegalNoticeRun(ctx, s.notice, run)
	return row.lifecycle(command.ExpectedRevision, true), nil
}

// lockForLifecycle takes the kind's activation lock and the version's row
// lock, then checks that the caller may still publish it. When
// expectedRevision is set the document must be at that revision.
func (s *Service) lockForLifecycle(ctx context.Context, tx *gorm.DB, kind *policyKind, id string, expectedRevision *string) (policyRow, error) {
	if err := s.legalOG.LockActivation(ctx, tx, kind.entity()); err != nil {
		return policyRow{}, err
	}
	row, err := lockPolicyRow(ctx, tx, kind, id)
	if err != nil {
		return row, err
	}
	policy, err := legalDocumentPolicyForType(kind.entity())
	if err != nil {
		return row, err
	}
	if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
		return row, err
	}
	if err := requireLegalPermission(ctx, s.spiceDB, kind.entity(), row.ID, legalMutationAction(policy, row.Status, legalActionPublish)); err != nil {
		return row, err
	}
	if expectedRevision == nil {
		return row, nil
	}
	return row, verifyLegalExpectedRevisionWithDB(ctx, tx, s.contentBlocks, kind.entity(), row.ID, *expectedRevision)
}

// CancelPolicyScheduleCommand returns a scheduled version to draft.
type CancelPolicyScheduleCommand struct {
	ID string
}

// CancelPolicySchedule returns the scheduled version to draft, cancels its
// notices and preview links, and moves the public route's OG back to the
// version that stays current.
func (s *Service) CancelPolicySchedule(ctx context.Context, kind PolicyKind, command CancelPolicyScheduleCommand) (PolicyLifecycle, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return PolicyLifecycle{}, err
	}
	var row policyRow
	var documentRevision string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if row, err = s.lockForLifecycle(ctx, tx, descriptor, command.ID, nil); err != nil {
			return err
		}
		if row.Status != string(descriptor.scheduled) {
			return domainerr.Precondition(descriptor.cancelNonScheduled)
		}
		if documentRevision, err = s.policyDocumentRevision(ctx, tx, descriptor, row); err != nil {
			return err
		}
		if err := cancelActiveLegalNoticeDeliveryRuns(ctx, tx, descriptor.entity(), row.ID); err != nil {
			return err
		}
		if err := deleteAutomaticLegalNoticePreviewShareLinks(ctx, tx, descriptor.entity(), row.ID); err != nil {
			return err
		}
		currentBefore, err := s.legalOG.CurrentForRoute(ctx, tx, descriptor.entity())
		if err != nil {
			return err
		}
		var cancelledEffectiveAt *time.Time
		if row.EffectiveFrom != nil {
			value := row.EffectiveFrom.UTC()
			cancelledEffectiveAt = &value
		}
		mutationNow := dbtime.Now()
		if err := tx.Table(descriptor.table).Where("id = ?", row.ID).Updates(structured.Fields{
			"status": descriptor.draft, "effective_from": nil, "updated_at": mutationNow,
		}).Error; err != nil {
			return err
		}
		row.Status, row.EffectiveFrom, row.UpdatedAt = string(descriptor.draft), nil, mutationNow
		if err := appendLegalPolicyLifecycleAudit(
			ctx, tx, s.auditWriter, descriptor.entity(), row.ID, row.Version,
			[]string{"effective_at", "status"}, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft,
			cancelledEffectiveAt, nil,
		); err != nil {
			return err
		}
		if currentBefore == nil || currentBefore.ID != row.ID {
			return nil
		}
		return s.refreshRouteAfterCancel(ctx, tx, descriptor)
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	return row.lifecycle(documentRevision, true), nil
}

func (s *Service) policyDocumentRevision(ctx context.Context, tx *gorm.DB, kind *policyKind, row policyRow) (string, error) {
	if row.ContentDocumentID == nil {
		return "", domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	documentID, err := parseLegalContentUUID("content_document_id", *row.ContentDocumentID)
	if err != nil {
		return "", err
	}
	revision, err := loadLegalDocumentRevisionWithDB(ctx, tx, documentID)
	if err != nil {
		return "", normalizeLegalContentBlockError(kind.entity(), err)
	}
	return revision.String(), nil
}

// refreshRouteAfterCancel regenerates the public route's OG for the version
// that is current after the cancelled one stopped being current, or
// releases it when none is.
func (s *Service) refreshRouteAfterCancel(ctx context.Context, tx *gorm.DB, kind *policyKind) error {
	current, err := s.legalOG.CurrentForRoute(ctx, tx, kind.entity())
	if err != nil {
		return err
	}
	if current != nil {
		return s.legalOG.RequestSaved(ctx, tx, kind.entity(), current.ID, "", true, kind.entity()+"_schedule_cancelled")
	}
	return s.legalOG.CancelAndRelease(ctx, tx, kind.entity(), s.legalOG.RouteID(kind.entity()))
}

// ActivatePolicyNowCommand activates a version immediately.
type ActivatePolicyNowCommand struct {
	ID               string
	ExpectedRevision string
}

// ActivatePolicyNow activates the version, archiving the active one, and
// seals the effective notice, which dispatches after the commit.
func (s *Service) ActivatePolicyNow(ctx context.Context, kind PolicyKind, command ActivatePolicyNowCommand) (PolicyLifecycle, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return PolicyLifecycle{}, err
	}
	now := time.Now()
	var row policyRow
	var effectiveRun *model.CampaignDeliveryRun
	policyURL := fmt.Sprintf("%s/%s", s.baseURL, descriptor.entity())

	// Policy state, the required sealed effective notice, and canonical OG
	// planning commit together. Provider dispatch remains post-commit.
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, descriptor.entity()); err != nil {
			return err
		}
		root, err := loadLegalContentDocumentRoot(ctx, tx, descriptor.entity(), command.ID, true)
		if err != nil {
			return err
		}
		policy, err := legalDocumentPolicyForType(descriptor.entity())
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(ctx, s.spiceDB, descriptor.entity(), root.ID, legalMutationAction(policy, root.Status, legalActionPublish)); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(ctx, tx, s.contentBlocks, descriptor.entity(), command.ID, command.ExpectedRevision); err != nil {
			return err
		}
		activated, run, err := ActivateAuditedLegalNoticeDocumentWithEffectiveRunWithDB(
			ctx, tx, s.auditWriter, s.legalOG, s.notice, descriptor.entity(), command.ID,
			LegalNoticeActivationImmediate, descriptor.effectiveEvent.String(),
			map[string]string{descriptor.effectiveURLKey: policyURL}, now,
		)
		if err != nil {
			return err
		}
		if !activated {
			return gorm.ErrRecordNotFound
		}
		if err := s.legalOG.RequestSaved(ctx, tx, descriptor.entity(), command.ID, "", true, descriptor.entity()+"_activated"); err != nil {
			return err
		}
		if err := tx.Table(descriptor.table).First(&row, "id = ?", command.ID).Error; err != nil {
			return err
		}
		effectiveRun = run
		return nil
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	DispatchCommittedLegalEffectiveNoticeAfterActivation(ctx, s.db, s.notice, descriptor.entity(), row.ID, now, effectiveRun)
	return row.lifecycle(command.ExpectedRevision, true), nil
}

// DeletePolicyCommand deletes a version in any lifecycle.
type DeletePolicyCommand struct {
	ID               string
	ExpectedRevision string
}

// DeletePolicy deletes the version, its document and preview links, keeps
// the delivery history of its notices, and moves the public route to the
// version that stays current.
func (s *Service) DeletePolicy(ctx context.Context, kind PolicyKind, command DeletePolicyCommand) error {
	descriptor, err := kind.descriptor()
	if err != nil {
		return err
	}
	var row policyRow
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := s.legalOG.LockActivation(ctx, tx, descriptor.entity()); err != nil {
			return err
		}
		if row, err = lockPolicyRow(ctx, tx, descriptor, command.ID); err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "delete", false); err != nil {
			return err
		}
		if err := requireLegalPermission(ctx, s.spiceDB, descriptor.entity(), row.ID, legalActionDelete); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(ctx, tx, s.contentBlocks, descriptor.entity(), row.ID, command.ExpectedRevision); err != nil {
			return err
		}
		if err := s.deletePolicyRow(ctx, tx, descriptor, row); err != nil {
			return err
		}
		if err := appendLegalPolicyIdentityAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLegalPolicyDeleted, descriptor.entity(), row.ID, row.Version); err != nil {
			return err
		}
		deletePolicy, err := descriptor.deletePolicy(row.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := descriptor.touchPolicy(row.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{deletePolicy}, []policyv1.RelationshipMutation{touchPolicy})
	})
	if err != nil {
		return err
	}
	if err := s.legalOG.ReleaseAssets(ctx, s.db, descriptor.entity(), row.ID); err != nil {
		slog.WarnContext(ctx, "Legal OG asset release failed after the document was deleted", "domain", "legal", "operation", "release_og_assets", "outcome", "failed", "entity_type", descriptor.entity(), "entity_id", row.ID, failurecause.Attr(err))
	}
	return nil
}

func (s *Service) deletePolicyRow(ctx context.Context, tx *gorm.DB, kind *policyKind, row policyRow) error {
	currentBefore, err := s.legalOG.CurrentForRoute(ctx, tx, kind.entity())
	if err != nil {
		return err
	}
	if err := cancelLegalNoticeDeliveryForPolicyDeletion(ctx, tx, kind.entity(), row.ID); err != nil {
		return err
	}
	if row.ContentDocumentID == nil {
		return domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	documentID, err := parseLegalContentUUID("content_document_id", *row.ContentDocumentID)
	if err != nil {
		return err
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, legalDocumentOwnershipFence(kind.entity(), row.ID)); err != nil {
		return normalizeLegalContentBlockError(kind.entity(), err)
	}
	if err := tx.Where("entity_type = ? AND entity_id = ?", kind.shareLinkEntityType, row.ID).Delete(&model.ShareLink{}).Error; err != nil {
		return err
	}
	if err := tx.Table(kind.table).Where("id = ?", row.ID).Delete(&policyRow{}).Error; err != nil {
		return err
	}
	return refreshLegalRouteAfterPolicyDeletion(ctx, tx, s.legalOG, kind.entity(), row.ID, currentBefore)
}

// RegeneratePolicyDerivedContentCommand rebuilds a version's HTML and text.
type RegeneratePolicyDerivedContentCommand struct {
	ID                     string
	ExpectedSnapshotDigest string
}

// RegeneratePolicyDerivedContent rebuilds HTML and text from the
// authoritative typed Block document and returns its snapshot digest.
func (s *Service) RegeneratePolicyDerivedContent(ctx context.Context, kind PolicyKind, command RegeneratePolicyDerivedContentCommand) (string, error) {
	if _, err := kind.descriptor(); err != nil {
		return "", err
	}
	return regenerateLegalDerivedContent(
		ctx, s.db, s.contentBlocks, s.spiceDB, s.legalOG,
		string(kind), command.ID, command.ExpectedSnapshotDigest,
	)
}

func (s *Service) policyWithDocumentInTransaction(ctx context.Context, kind *policyKind, id string) (Policy, error) {
	return s.policyWithDocument(ctx, s.db.WithContext(ctx), kind, id)
}

// policyWithDocument reads the version under a share lock with its source
// locale document, in a transaction of db (a savepoint when db is one).
func (s *Service) policyWithDocument(ctx context.Context, db *gorm.DB, kind *policyKind, id string) (Policy, error) {
	var persisted policyRow
	var sourceLocale string
	var snapshot contentblock.Snapshot
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(kind.table).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
			First(&persisted, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.ResourceNotFound(kind.entity(), id)
			}
			return err
		}
		if persisted.ContentDocumentID == nil {
			return domainerr.Precondition(kind.entity() + " content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *persisted.ContentDocumentID)
		if err != nil {
			return err
		}
		root, err := loadLegalContentDocumentRoot(ctx, tx, kind.entity(), id, false)
		if err != nil {
			return err
		}
		sourceLocale = root.SourceLocale
		snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
		return err
	})
	if err != nil {
		return Policy{}, normalizeLegalContentBlockError(kind.entity(), err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return Policy{}, normalizeLegalContentBlockError(kind.entity(), err)
	}
	return Policy{
		ID: persisted.ID, Version: int32(persisted.Version), Title: persisted.Title,
		Document: document, Revision: snapshot.Document.Revision.String(),
		SourceLocale: sourceLocale, SnapshotDigest: snapshot.SnapshotDigest,
		Status:    model.LegalPolicyStatus(persisted.Status),
		CreatedAt: persisted.CreatedAt, UpdatedAt: persisted.UpdatedAt,
		EffectiveFrom: persisted.EffectiveFrom, EffectiveUntil: persisted.EffectiveUntil,
	}, nil
}

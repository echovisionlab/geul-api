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
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	PreviewLinks  PreviewLinks
	Members       MemberLocales
	Audit         domainaudit.Appender
	// BaseURL is the site origin of notice links; empty keeps them relative.
	BaseURL string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil,
		"OG": d.OG == nil, "Notice": d.Notice == nil, "PreviewLinks": d.PreviewLinks == nil,
		"Members": d.Members == nil, "Audit": d.Audit == nil,
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
	previewLinks  PreviewLinks
	members       MemberLocales
	contentBlocks *contentblock.Store
	legalOG       OG
	mutations     *mutation.Runner
}

// NewService validates deps and returns the legal policy use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:            deps.DB,
		spiceDB:       deps.SpiceDB,
		baseURL:       deps.BaseURL,
		notice:        deps.Notice,
		previewLinks:  deps.PreviewLinks,
		members:       deps.Members,
		contentBlocks: deps.ContentBlocks,
		legalOG:       deps.OG,
		mutations:     mutations,
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
	err = readLocked(ctx, s.mutations, mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		var row policyRow
		if err := tx.Table(descriptor.table).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).First(&row, "id = ?", query.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.ResourceNotFound(descriptor.notFoundResource, query.ID)
			}
			return err
		}
		return requireLegalVersionViewOrNotFound(ctx, tx, s.spiceDB, descriptor.entity(), row.ID, row.Status)
	}), func(ctx context.Context, tx *gorm.DB) error {
		policy, err = s.policyWithDocument(ctx, tx, descriptor, query.ID)
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
	if err := readLocked(ctx, s.mutations, authorizedBefore, func(ctx context.Context, tx *gorm.DB) error {
		for i, row := range rows {
			if versions[i], err = s.policyWithDocument(ctx, tx, descriptor, row.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return PolicyPage{}, err
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
	row, err := mutation.Run(ctx, s.mutations, mutation.Spec[policyRow]{
		// Serialize version assignment.
		Lock: func(_ context.Context, tx *gorm.DB) error {
			return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", descriptor.table+"_version").Error
		},
		Access: mutation.FreshAdminCan(descriptor.create()),
		Change: func(ctx context.Context, tx *gorm.DB) (policyRow, error) {
			return s.createDraftVersion(ctx, tx, descriptor, command)
		},
		Audit: func(row policyRow) mutation.Audit {
			return policyIdentityAudit(sharedtelemetry.AuditLegalPolicyCreated, descriptor, row)
		},
		Relationships: func(row policyRow) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return descriptor.policyRelationships(row.ID, true)
		},
	})
	if err != nil {
		return Policy{}, err
	}
	return s.readPolicy(ctx, descriptor, row.ID)
}

// policyRelationships returns the version's policy relationship write and
// its exact inverse: the touch when grant is set, the delete otherwise.
func (k *policyKind) policyRelationships(id string, grant bool) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	touchPolicy, err := k.touchPolicy(id)
	if err != nil {
		return nil, nil, err
	}
	deletePolicy, err := k.deletePolicy(id)
	if err != nil {
		return nil, nil, err
	}
	if grant {
		return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, nil
	}
	return []policyv1.RelationshipMutation{deletePolicy}, []policyv1.RelationshipMutation{touchPolicy}, nil
}

func (s *Service) createDraftVersion(ctx context.Context, tx *gorm.DB, kind *policyKind, command CreatePolicyVersionCommand) (policyRow, error) {
	var row policyRow
	title := kind.defaultTitle
	if command.Title != nil && *command.Title != "" {
		title = *command.Title
	}
	now := dbtime.Now()
	sourceLocale := resolveInitialSourceLocale(ctx, tx, s.members, command.AcceptLanguage)
	created, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile: legalContentDocumentProfile, SourceLocale: sourceLocale,
	})
	if err != nil {
		return row, normalizeLegalContentBlockError(kind.entity(), err)
	}
	// The version follows both the history and the notices sealed for
	// deleted versions, so a version number is never reused.
	sealedVersion, err := s.notice.LatestSealedVersion(ctx, tx, kind.entity())
	if err != nil {
		return row, err
	}
	if err := tx.Raw(`
		INSERT INTO `+kind.table+` (title, content, status, version, created_at, updated_at, content_document_id, source_locale)
		VALUES (?, '', ?, GREATEST(COALESCE((SELECT MAX(version) FROM `+kind.table+`), 0), ?::integer) + 1, ?, ?, ?, ?)
		RETURNING id, version, title, content, status, created_at, updated_at, content_document_id
	`, title, kind.draft, sealedVersion, now, now, created.Document.ID.String(), sourceLocale).Scan(&row).Error; err != nil {
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
	return row, refreshDerivedContent(ctx, tx, kind.entity(), row.ID, finalSnapshot, sourceLocale, now)
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
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: s.lifecycleAccess(descriptor, command.ID, &row),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			var err error
			run, err = s.schedule(ctx, tx, descriptor, &row, command.ExpectedRevision, effectiveFrom, previewURL, now)
			return struct{}{}, err
		},
		Audit: func(struct{}) mutation.Audit {
			return legalLifecycleChange{
				kind: descriptor.entity(), policyID: row.ID, version: row.Version,
				changedFields: []string{"effective_at", "status"},
				previous:      sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStateScheduled,
				effectiveAt: &effectiveFrom,
			}.audit()
		},
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	dispatchCreatedLegalNoticeRun(ctx, s.notice, run)
	return row.lifecycle(command.ExpectedRevision, true), nil
}

// schedule moves the locked draft row to scheduled at effectiveFrom and
// seals its update notice.
func (s *Service) schedule(
	ctx context.Context,
	tx *gorm.DB,
	kind *policyKind,
	row *policyRow,
	expectedRevision string,
	effectiveFrom time.Time,
	previewURL string,
	now time.Time,
) (*model.CampaignDeliveryRun, error) {
	if err := verifyLegalExpectedRevisionWithDB(ctx, tx, kind.entity(), row.ID, expectedRevision); err != nil {
		return nil, err
	}
	if row.Status != string(kind.draft) {
		return nil, domainerr.Precondition(kind.scheduleNonDraft)
	}
	if err := ensureScheduleSlot(ctx, tx, kind, row.ID); err != nil {
		return nil, err
	}
	mutationNow := dbtime.Now()
	if err := tx.Table(kind.table).Where("id = ?", row.ID).Updates(structured.Fields{
		"status": kind.scheduled, "effective_from": effectiveFrom, "updated_at": mutationNow,
	}).Error; err != nil {
		return nil, err
	}
	row.Status, row.EffectiveFrom, row.UpdatedAt = string(kind.scheduled), &effectiveFrom, mutationNow
	if err := s.legalOG.RequestSaved(ctx, tx, kind.entity(), row.ID, "", true, kind.entity()+"_scheduled"); err != nil {
		return nil, err
	}
	return s.notice.CreateRun(
		ctx, tx, kind.entity(), row.ID, kind.updateEvent.String(),
		map[string]string{
			"policy_title":   row.Title,
			"effective_date": effectiveFrom.Format("2006-01-02"),
			"preview_url":    previewURL,
		},
		legalNoticeDispatchAt(now, effectiveFrom),
	)
}

// lifecycleAccess takes the kind's activation lock and the version's row
// lock into row, then checks that the caller may still publish it.
func (s *Service) lifecycleAccess(kind *policyKind, id string, row *policyRow) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		if err := s.legalOG.LockActivation(ctx, tx, kind.entity()); err != nil {
			return err
		}
		var err error
		if *row, err = lockPolicyRow(ctx, tx, kind, id); err != nil {
			return err
		}
		return authorizeLegal(ctx, tx, s.spiceDB, kind.entity(), row.ID, "publish", false, legalMutationAction(kind, row.Status, legalActionPublish))
	})
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
	var cancelledEffectiveAt *time.Time
	documentRevision, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Access: s.lifecycleAccess(descriptor, command.ID, &row),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			if row.EffectiveFrom != nil {
				value := row.EffectiveFrom.UTC()
				cancelledEffectiveAt = &value
			}
			return s.cancelSchedule(ctx, tx, descriptor, &row)
		},
		Audit: func(string) mutation.Audit {
			return legalLifecycleChange{
				kind: descriptor.entity(), policyID: row.ID, version: row.Version,
				changedFields: []string{"effective_at", "status"},
				previous:      sharedtelemetry.AuditStateScheduled, next: sharedtelemetry.AuditStateDraft,
				effectiveAt: cancelledEffectiveAt,
			}.audit()
		},
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	return row.lifecycle(documentRevision, true), nil
}

// cancelSchedule returns the locked scheduled row to draft and returns its
// document revision.
func (s *Service) cancelSchedule(ctx context.Context, tx *gorm.DB, kind *policyKind, row *policyRow) (string, error) {
	if row.Status != string(kind.scheduled) {
		return "", domainerr.Precondition(kind.cancelNonScheduled)
	}
	documentRevision, err := s.policyDocumentRevision(ctx, tx, kind, *row)
	if err != nil {
		return "", err
	}
	if err := s.notice.CancelScheduledRuns(ctx, tx, kind.entity(), row.ID); err != nil {
		return "", err
	}
	if err := s.deleteAutomaticNoticePreviewLinks(ctx, tx, kind, row.ID); err != nil {
		return "", err
	}
	currentBefore, err := s.legalOG.CurrentForRoute(ctx, tx, kind.entity())
	if err != nil {
		return "", err
	}
	mutationNow := dbtime.Now()
	if err := tx.Table(kind.table).Where("id = ?", row.ID).Updates(structured.Fields{
		"status": kind.draft, "effective_from": nil, "updated_at": mutationNow,
	}).Error; err != nil {
		return "", err
	}
	row.Status, row.EffectiveFrom, row.UpdatedAt = string(kind.draft), nil, mutationNow
	if currentBefore == nil || currentBefore.ID != row.ID {
		return documentRevision, nil
	}
	return documentRevision, s.refreshRouteAfterCancel(ctx, tx, kind)
}

func (s *Service) policyDocumentRevision(ctx context.Context, tx *gorm.DB, kind *policyKind, row policyRow) (string, error) {
	if row.ContentDocumentID == nil {
		return "", domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	documentID, err := parseLegalContentUUID("content_document_id", *row.ContentDocumentID)
	if err != nil {
		return "", err
	}
	revision, err := contentblock.LoadRevision(ctx, tx, documentID)
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
	// Policy state, the required sealed effective notice, and canonical OG
	// planning commit together. Provider dispatch remains post-commit.
	activation, err := mutation.Run(ctx, s.mutations, mutation.Spec[policyActivation]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			if err := s.legalOG.LockActivation(ctx, tx, descriptor.entity()); err != nil {
				return err
			}
			root, err := loadLegalContentDocumentRoot(ctx, tx, descriptor.entity(), command.ID, true)
			if err != nil {
				return err
			}
			return authorizeLegal(ctx, tx, s.spiceDB, descriptor.entity(), root.ID, "publish", false, legalMutationAction(descriptor, root.Status, legalActionPublish))
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (policyActivation, error) {
			var activation policyActivation
			var err error
			activation, row, effectiveRun, err = s.activateNow(ctx, tx, descriptor, command, now)
			return activation, err
		},
		Audit: func(activation policyActivation) mutation.Audit {
			changes := activation.lifecycleChanges()
			audits := make([]mutation.Audit, len(changes))
			for i, change := range changes {
				audits[i] = change.audit()
			}
			return mutation.Audits(audits...)
		},
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	DispatchCommittedLegalEffectiveNoticeAfterActivation(ctx, s.db, s.notice, descriptor.entity(), activation.policyID, now, effectiveRun)
	return row.lifecycle(command.ExpectedRevision, true), nil
}

// activateNow activates the authorized version, seals its effective notice,
// requests its OG, and returns the activated row.
func (s *Service) activateNow(
	ctx context.Context,
	tx *gorm.DB,
	kind *policyKind,
	command ActivatePolicyNowCommand,
	now time.Time,
) (policyActivation, policyRow, *model.CampaignDeliveryRun, error) {
	var row policyRow
	if err := verifyLegalExpectedRevisionWithDB(ctx, tx, kind.entity(), command.ID, command.ExpectedRevision); err != nil {
		return policyActivation{}, row, nil, err
	}
	activation, _, err := activatePolicyVersion(ctx, tx, s.legalOG, kind, command.ID, true, now)
	if err != nil {
		return policyActivation{}, row, nil, err
	}
	run, err := s.notice.CreateRun(
		ctx, tx, kind.entity(), command.ID, kind.effectiveEvent.String(),
		map[string]string{kind.effectiveURLKey: fmt.Sprintf("%s/%s", s.baseURL, kind.entity())}, now,
	)
	if err != nil {
		return policyActivation{}, row, nil, err
	}
	if err := s.legalOG.RequestSaved(ctx, tx, kind.entity(), command.ID, "", true, kind.entity()+"_activated"); err != nil {
		return policyActivation{}, row, nil, err
	}
	err = tx.Table(kind.table).First(&row, "id = ?", command.ID).Error
	return activation, row, run, err
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
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			if err := s.legalOG.LockActivation(ctx, tx, descriptor.entity()); err != nil {
				return err
			}
			var err error
			if row, err = lockPolicyRow(ctx, tx, descriptor, command.ID); err != nil {
				return err
			}
			return authorizeLegal(ctx, tx, s.spiceDB, descriptor.entity(), row.ID, "delete", false, legalActionDelete)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			if err := verifyLegalExpectedRevisionWithDB(ctx, tx, descriptor.entity(), row.ID, command.ExpectedRevision); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, s.deletePolicyRow(ctx, tx, descriptor, row)
		},
		Audit: func(struct{}) mutation.Audit {
			return policyIdentityAudit(sharedtelemetry.AuditLegalPolicyDeleted, descriptor, row)
		},
		Relationships: func(struct{}) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return descriptor.policyRelationships(row.ID, false)
		},
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
	if err := s.notice.StopRunsForDeletedPolicy(ctx, tx, kind.entity(), row.ID); err != nil {
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
	if err := s.previewLinks.DeleteForEntity(ctx, tx, kind.shareLinkEntityType, row.ID); err != nil {
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
// Derived content changes no policy fact, so it records no audit.
func (s *Service) RegeneratePolicyDerivedContent(ctx context.Context, kind PolicyKind, command RegeneratePolicyDerivedContentCommand) (string, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return "", err
	}
	var root legalContentDocumentRoot
	return mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			var err error
			if root, err = loadLegalContentDocumentRoot(ctx, tx, descriptor.entity(), command.ID, true); err != nil {
				return err
			}
			return authorizeLegal(ctx, tx, s.spiceDB, descriptor.entity(), command.ID, "manage", false, legalMutationAction(descriptor, root.Status, legalActionManage))
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, root.SourceLocale)
			if err != nil {
				return "", normalizeLegalContentBlockError(descriptor.entity(), err)
			}
			if strings.TrimSpace(command.ExpectedSnapshotDigest) == "" || command.ExpectedSnapshotDigest != snapshot.SnapshotDigest {
				return "", domainerr.Precondition("legal derived-content regeneration targets a stale Block document")
			}
			if err := refreshDerivedContent(ctx, tx, descriptor.entity(), command.ID, snapshot, root.SourceLocale, time.Now().UTC()); err != nil {
				return "", err
			}
			return snapshot.SnapshotDigest, s.legalOG.RequestSaved(ctx, tx, descriptor.entity(), command.ID, "", true, descriptor.entity()+"_derived_content_regenerated")
		},
		Audit: func(string) mutation.Audit { return mutation.Unchanged() },
	})
}

// readPolicy reads the version with its document after a mutation
// committed.
func (s *Service) readPolicy(ctx context.Context, kind *policyKind, id string) (Policy, error) {
	var policy Policy
	err := readLocked(ctx, s.mutations, authorizedBefore, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		policy, err = s.policyWithDocument(ctx, tx, kind, id)
		return err
	})
	return policy, err
}

// policyWithDocument reads the version under a share lock with its source
// locale document.
func (s *Service) policyWithDocument(ctx context.Context, tx *gorm.DB, kind *policyKind, id string) (Policy, error) {
	var persisted policyRow
	if err := tx.Table(kind.table).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		First(&persisted, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Policy{}, domainerr.ResourceNotFound(kind.entity(), id)
		}
		return Policy{}, err
	}
	if persisted.ContentDocumentID == nil {
		return Policy{}, domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	documentID, err := parseLegalContentUUID("content_document_id", *persisted.ContentDocumentID)
	if err != nil {
		return Policy{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, tx, kind.entity(), id, false)
	if err != nil {
		return Policy{}, err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, root.SourceLocale)
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
		SourceLocale: root.SourceLocale, SnapshotDigest: snapshot.SnapshotDigest,
		Status:    model.LegalPolicyStatus(persisted.Status),
		CreatedAt: persisted.CreatedAt, UpdatedAt: persisted.UpdatedAt,
		EffectiveFrom: persisted.EffectiveFrom, EffectiveUntil: persisted.EffectiveUntil,
	}, nil
}

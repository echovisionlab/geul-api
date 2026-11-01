package legal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// NoticeDispatch hands a sealed notice run to Campaign's delivery
// dispatcher after the policy commit, and cancels the update notice a
// committed activation made obsolete. Both are best-effort: provider
// availability is not part of a policy's legal effect.
type NoticeDispatch interface {
	DispatchRun(ctx context.Context, runID string) error
	// CancelUpdateNoticeAfterActivation cancels the still-scheduled update
	// notice of a version that became effective; it logs a failure.
	CancelUpdateNoticeAfterActivation(ctx context.Context, kind PolicyKind, id string, now time.Time)
}

// OGAssets releases the OG images of a deleted version after its commit.
type OGAssets interface {
	ReleaseOGAssets(ctx context.Context, kind PolicyKind, id string) error
}

// Deps are the collaborators of the Terms and Privacy management use cases.
// Every field except BaseURL is required; NewService rejects a missing one.
type Deps struct {
	Units    mutation.UnitOfWork[Store] // legalpostgres.NewUnits
	SpiceDB  mutation.Authorizer
	Fence    identitystate.Fence
	Audit    domainaudit.Appender
	Dispatch NoticeDispatch
	OGAssets OGAssets
	// BaseURL is the site origin of notice links; empty keeps them relative.
	BaseURL string
}

func (d Deps) validate() error {
	return requireDeps("legal dependencies are required: ", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Dispatch": d.Dispatch == nil, "OGAssets": d.OGAssets == nil,
	})
}

// requireDeps names every absent collaborator after prefix.
func requireDeps(prefix string, absent map[string]bool) error {
	var missing []string
	for name, isAbsent := range absent {
		if isAbsent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New(prefix + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Terms and Privacy management use cases. Each takes the
// PolicyKind it acts on.
type Service struct {
	spiceDB   mutation.Authorizer
	baseURL   string
	dispatch  NoticeDispatch
	ogAssets  OGAssets
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the legal policy use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		spiceDB: deps.SpiceDB, baseURL: deps.BaseURL, dispatch: deps.Dispatch, ogAssets: deps.OGAssets,
		mutations: mutations,
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
	Status         Status
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
	Status           Status
	UpdatedAt        time.Time
	EffectiveFrom    *time.Time
	EffectiveUntil   *time.Time
}

func (version Version) lifecycle(documentRevision string, changed bool) PolicyLifecycle {
	return PolicyLifecycle{
		ID: version.ID, Changed: changed, DocumentRevision: documentRevision,
		Status: version.Status, UpdatedAt: version.UpdatedAt,
		EffectiveFrom: version.EffectiveFrom, EffectiveUntil: version.EffectiveUntil,
	}
}

// lockVersion locks the version for update. A missing version answers
// NotFound in the kind's wording.
func lockVersion(ctx context.Context, store Store, kind *policyKind, id string) (Version, error) {
	version, err := store.Version(ctx, kind.kind, id, UpdateLock)
	if errors.Is(err, ErrPolicyNotFound) {
		return version, domainerr.ResourceNotFound(kind.notFoundResource, id)
	}
	return version, err
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
	err = readLocked(ctx, s.mutations, mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		version, err := store.Version(ctx, kind, query.ID, ShareLock)
		if err != nil {
			if errors.Is(err, ErrPolicyNotFound) {
				return domainerr.ResourceNotFound(descriptor.notFoundResource, query.ID)
			}
			return err
		}
		return requireLegalVersionViewOrNotFound(ctx, s.spiceDB, lockActive, descriptor, version.ID, version.Status)
	}), func(ctx context.Context, store Store) error {
		policy, err = policyWithDocument(ctx, store, descriptor, query.ID)
		return err
	})
	return policy, err
}

// ListPolicyVersionsQuery pages through versions, newest first.
type ListPolicyVersionsQuery struct {
	// Status keeps only versions in that state when set.
	Status *Status
	// Limit defaults to 20.
	Limit  int32
	Offset int32
}

// PolicyPage is one page of versions and where it lies among them.
type PolicyPage struct {
	Versions []Policy
	Total    int32
	Limit    int32
	Offset   int32
	HasMore  bool
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
	limit := int32(20)
	if query.Limit > 0 {
		limit = query.Limit
	}
	var total int64
	var versions []Policy
	if err := readLocked(ctx, s.mutations, authorizedBefore, func(ctx context.Context, store Store) error {
		var rows []Version
		var err error
		if rows, total, err = store.ListVersions(ctx, kind, query.Status, limit, query.Offset); err != nil {
			return err
		}
		versions = make([]Policy, len(rows))
		for i, row := range rows {
			if versions[i], err = policyWithDocument(ctx, store, descriptor, row.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return PolicyPage{}, err
	}
	return PolicyPage{
		Versions: versions, Total: int32(total), Limit: limit, Offset: query.Offset,
		HasMore: query.Offset+limit < int32(total),
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
	version, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Version, Store]{
		// Serialize version assignment.
		Lock: func(ctx context.Context, store Store) error {
			return store.LockVersionAssignment(ctx, kind)
		},
		Access: mutation.FreshAdminCan(descriptor.create()),
		Change: func(ctx context.Context, store Store) (Version, error) {
			return createDraftVersion(ctx, store, descriptor, command)
		},
		Audit: func(version Version) mutation.Audit {
			return policyIdentityAudit(sharedtelemetry.AuditLegalPolicyCreated, descriptor, version)
		},
		Relationships: func(version Version) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return descriptor.policyRelationships(version.ID, true)
		},
	})
	if err != nil {
		return Policy{}, err
	}
	return s.readPolicy(ctx, descriptor, version.ID)
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

func createDraftVersion(ctx context.Context, store Store, kind *policyKind, command CreatePolicyVersionCommand) (Version, error) {
	title := kind.defaultTitle
	if command.Title != nil && *command.Title != "" {
		title = *command.Title
	}
	now := dbtime.Now()
	sourceLocale := resolveInitialSourceLocale(ctx, store, command.AcceptLanguage)
	created, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile: legalContentDocumentProfile, SourceLocale: sourceLocale,
	})
	if err != nil {
		return Version{}, normalizeLegalContentBlockError(kind.entity(), err)
	}
	// The version follows both the history and the notices sealed for
	// deleted versions, so a version number is never reused.
	sealedVersion, err := store.LatestSealedVersion(ctx, kind.kind)
	if err != nil {
		return Version{}, err
	}
	version, err := store.InsertDraft(ctx, kind.kind, Draft{
		Title: title, SealedVersion: sealedVersion, ContentDocumentID: created.Document.ID.String(),
		SourceLocale: sourceLocale, CreatedAt: now,
	})
	if err != nil {
		return version, err
	}
	if command.Document != nil {
		if command.Document.GetSourceLocale() != sourceLocale {
			return version, domainerr.InvalidField("document.source_locale", "must match the server-selected source locale")
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, command.Document)
		if err != nil {
			return version, normalizeLegalContentBlockError(kind.entity(), err)
		}
		if _, err := store.ReplaceContentSnapshot(ctx, replacement, legalDocumentOwnershipFence(kind, version.ID)); err != nil {
			return version, normalizeLegalContentBlockError(kind.entity(), err)
		}
	}
	finalSnapshot, err := store.LockedContentSnapshot(ctx, created.Document.ID, sourceLocale)
	if err != nil {
		return version, normalizeLegalContentBlockError(kind.entity(), err)
	}
	return version, refreshDerivedContent(ctx, store, kind, version.ID, finalSnapshot, sourceLocale, now)
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
	var version Version
	var run *NoticeRun
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: s.lifecycleAccess(descriptor, command.ID, &version),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			var err error
			run, err = schedule(ctx, store, descriptor, &version, command.ExpectedRevision, effectiveFrom, previewURL, now)
			return struct{}{}, err
		},
		Audit: func(struct{}) mutation.Audit {
			return legalLifecycleChange{
				kind: descriptor.entity(), policyID: version.ID, version: version.Version,
				changedFields: []string{"effective_at", "status"},
				previous:      sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStateScheduled,
				effectiveAt: &effectiveFrom,
			}.audit()
		},
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	dispatchCreatedLegalNoticeRun(ctx, s.dispatch, run)
	return version.lifecycle(command.ExpectedRevision, true), nil
}

// schedule moves the locked draft version to scheduled at effectiveFrom and
// seals its update notice.
func schedule(
	ctx context.Context,
	store Store,
	kind *policyKind,
	version *Version,
	expectedRevision string,
	effectiveFrom time.Time,
	previewURL string,
	now time.Time,
) (*NoticeRun, error) {
	if err := verifyLegalExpectedRevision(ctx, store, kind, version.ID, expectedRevision); err != nil {
		return nil, err
	}
	if version.Status != kind.draft {
		return nil, domainerr.Precondition(kind.scheduleNonDraft)
	}
	if err := ensureScheduleSlot(ctx, store, kind, version.ID); err != nil {
		return nil, err
	}
	mutationNow := dbtime.Now()
	if err := store.Schedule(ctx, kind.kind, version.ID, effectiveFrom, mutationNow); err != nil {
		return nil, err
	}
	version.Status, version.EffectiveFrom, version.UpdatedAt = kind.scheduled, &effectiveFrom, mutationNow
	if err := store.RequestOG(ctx, kind.kind, version.ID, "", true, kind.entity()+"_scheduled"); err != nil {
		return nil, err
	}
	return store.SealNoticeRun(
		ctx, kind.kind, version.ID, kind.updateEvent.String(),
		map[string]string{
			"policy_title":   version.Title,
			"effective_date": effectiveFrom.Format("2006-01-02"),
			"preview_url":    previewURL,
		},
		legalNoticeDispatchAt(now, effectiveFrom),
	)
}

// lifecycleAccess takes the kind's activation lock and the version's row
// lock into version, then checks that the caller may still publish it.
func (s *Service) lifecycleAccess(kind *policyKind, id string, version *Version) mutation.Access {
	return mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		if err := store.LockActivation(ctx, kind.kind); err != nil {
			return err
		}
		var err error
		if *version, err = lockVersion(ctx, store, kind, id); err != nil {
			return err
		}
		return authorizeLegal(ctx, s.spiceDB, lockActive, kind, version.ID, "publish", false, legalMutationAction(kind, version.Status, legalActionPublish))
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
	var version Version
	var cancelledEffectiveAt *time.Time
	documentRevision, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Access: s.lifecycleAccess(descriptor, command.ID, &version),
		Change: func(ctx context.Context, store Store) (string, error) {
			if version.EffectiveFrom != nil {
				value := version.EffectiveFrom.UTC()
				cancelledEffectiveAt = &value
			}
			return cancelSchedule(ctx, store, descriptor, &version)
		},
		Audit: func(string) mutation.Audit {
			return legalLifecycleChange{
				kind: descriptor.entity(), policyID: version.ID, version: version.Version,
				changedFields: []string{"effective_at", "status"},
				previous:      sharedtelemetry.AuditStateScheduled, next: sharedtelemetry.AuditStateDraft,
				effectiveAt: cancelledEffectiveAt,
			}.audit()
		},
	})
	if err != nil {
		return PolicyLifecycle{}, err
	}
	return version.lifecycle(documentRevision, true), nil
}

// cancelSchedule returns the locked scheduled version to draft and returns
// its document revision.
func cancelSchedule(ctx context.Context, store Store, kind *policyKind, version *Version) (string, error) {
	if version.Status != kind.scheduled {
		return "", domainerr.Precondition(kind.cancelNonScheduled)
	}
	documentRevision, err := policyDocumentRevision(ctx, store, kind, *version)
	if err != nil {
		return "", err
	}
	if err := store.CancelScheduledNotices(ctx, kind.kind, version.ID); err != nil {
		return "", err
	}
	if err := deleteAutomaticNoticePreviewLinks(ctx, store, kind, version.ID); err != nil {
		return "", err
	}
	currentBefore, err := store.CurrentRoute(ctx, kind.kind)
	if err != nil {
		return "", err
	}
	mutationNow := dbtime.Now()
	if err := store.Unschedule(ctx, kind.kind, version.ID, mutationNow); err != nil {
		return "", err
	}
	version.Status, version.EffectiveFrom, version.UpdatedAt = kind.draft, nil, mutationNow
	if currentBefore == nil || currentBefore.ID != version.ID {
		return documentRevision, nil
	}
	return documentRevision, refreshRouteAfterCancel(ctx, store, kind)
}

func policyDocumentRevision(ctx context.Context, store Store, kind *policyKind, version Version) (string, error) {
	if version.ContentDocumentID == nil {
		return "", domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	documentID, err := parseLegalContentUUID("content_document_id", *version.ContentDocumentID)
	if err != nil {
		return "", err
	}
	revision, err := store.ContentRevision(ctx, documentID)
	if err != nil {
		return "", normalizeLegalContentBlockError(kind.entity(), err)
	}
	return revision.String(), nil
}

// refreshRouteAfterCancel regenerates the public route's OG for the version
// that is current after the cancelled one stopped being current, or
// releases it when none is.
func refreshRouteAfterCancel(ctx context.Context, store Store, kind *policyKind) error {
	current, err := store.CurrentRoute(ctx, kind.kind)
	if err != nil {
		return err
	}
	if current != nil {
		return store.RequestOG(ctx, kind.kind, current.ID, "", true, kind.entity()+"_schedule_cancelled")
	}
	return store.ReleaseRoute(ctx, kind.kind)
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
	var version Version
	var effectiveRun *NoticeRun
	// Policy state, the required sealed effective notice, and canonical OG
	// planning commit together. Provider dispatch remains post-commit.
	activation, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[policyActivation, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			if err := store.LockActivation(ctx, kind); err != nil {
				return err
			}
			root, err := loadLegalContentDocumentRoot(ctx, store, descriptor, command.ID, true)
			if err != nil {
				return err
			}
			return authorizeLegal(ctx, s.spiceDB, lockActive, descriptor, root.ID, "publish", false, legalMutationAction(descriptor, root.Status, legalActionPublish))
		}),
		Change: func(ctx context.Context, store Store) (policyActivation, error) {
			var activation policyActivation
			var err error
			activation, version, effectiveRun, err = s.activateNow(ctx, store, descriptor, command, now)
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
	DispatchCommittedEffectiveNotice(ctx, s.dispatch, kind, activation.policyID, now, effectiveRun)
	return version.lifecycle(command.ExpectedRevision, true), nil
}

// activateNow activates the authorized version, seals its effective notice,
// requests its OG, and returns the activated version.
func (s *Service) activateNow(
	ctx context.Context,
	store Store,
	kind *policyKind,
	command ActivatePolicyNowCommand,
	now time.Time,
) (policyActivation, Version, *NoticeRun, error) {
	if err := verifyLegalExpectedRevision(ctx, store, kind, command.ID, command.ExpectedRevision); err != nil {
		return policyActivation{}, Version{}, nil, err
	}
	activation, _, err := activatePolicyVersion(ctx, store, kind, command.ID, true, now)
	if err != nil {
		return policyActivation{}, Version{}, nil, err
	}
	run, err := store.SealNoticeRun(
		ctx, kind.kind, command.ID, kind.effectiveEvent.String(),
		map[string]string{kind.effectiveURLKey: fmt.Sprintf("%s/%s", s.baseURL, kind.entity())}, now,
	)
	if err != nil {
		return policyActivation{}, Version{}, nil, err
	}
	if err := store.RequestOG(ctx, kind.kind, command.ID, "", true, kind.entity()+"_activated"); err != nil {
		return policyActivation{}, Version{}, nil, err
	}
	version, err := store.Version(ctx, kind.kind, command.ID, NoLock)
	return activation, version, run, err
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
	var version Version
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			if err := store.LockActivation(ctx, kind); err != nil {
				return err
			}
			var err error
			if version, err = lockVersion(ctx, store, descriptor, command.ID); err != nil {
				return err
			}
			return authorizeLegal(ctx, s.spiceDB, lockActive, descriptor, version.ID, "delete", false, legalActionDelete)
		}),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			if err := verifyLegalExpectedRevision(ctx, store, descriptor, version.ID, command.ExpectedRevision); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, deleteVersion(ctx, store, descriptor, version)
		},
		Audit: func(struct{}) mutation.Audit {
			return policyIdentityAudit(sharedtelemetry.AuditLegalPolicyDeleted, descriptor, version)
		},
		Relationships: func(struct{}) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return descriptor.policyRelationships(version.ID, false)
		},
	})
	if err != nil {
		return err
	}
	if err := s.ogAssets.ReleaseOGAssets(ctx, kind, version.ID); err != nil {
		slog.WarnContext(ctx, "Legal OG asset release failed after the document was deleted", "domain", "legal", "operation", "release_og_assets", "outcome", "failed", "entity_type", descriptor.entity(), "entity_id", version.ID, failurecause.Attr(err))
	}
	return nil
}

func deleteVersion(ctx context.Context, store Store, kind *policyKind, version Version) error {
	currentBefore, err := store.CurrentRoute(ctx, kind.kind)
	if err != nil {
		return err
	}
	if err := store.StopNoticesOfDeletedVersion(ctx, kind.kind, version.ID); err != nil {
		return err
	}
	if version.ContentDocumentID == nil {
		return domainerr.Precondition(kind.entity() + " content document has not been populated")
	}
	documentID, err := parseLegalContentUUID("content_document_id", *version.ContentDocumentID)
	if err != nil {
		return err
	}
	if err := store.DeleteContentDocument(ctx, documentID, legalDocumentOwnershipFence(kind, version.ID)); err != nil {
		return normalizeLegalContentBlockError(kind.entity(), err)
	}
	if err := store.DeletePreviewLinks(ctx, kind.shareLinkEntityType, version.ID); err != nil {
		return err
	}
	if err := store.DeleteVersion(ctx, kind.kind, version.ID); err != nil {
		return err
	}
	return refreshLegalRouteAfterPolicyDeletion(ctx, store, kind, version.ID, currentBefore)
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
	var root Root
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			var err error
			if root, err = loadLegalContentDocumentRoot(ctx, store, descriptor, command.ID, true); err != nil {
				return err
			}
			return authorizeLegal(ctx, s.spiceDB, lockActive, descriptor, command.ID, "manage", false, legalMutationAction(descriptor, root.Status, legalActionManage))
		}),
		Change: func(ctx context.Context, store Store) (string, error) {
			snapshot, err := store.LockedContentSnapshot(ctx, *root.ContentDocumentID, root.SourceLocale)
			if err != nil {
				return "", normalizeLegalContentBlockError(descriptor.entity(), err)
			}
			if strings.TrimSpace(command.ExpectedSnapshotDigest) == "" || command.ExpectedSnapshotDigest != snapshot.SnapshotDigest {
				return "", domainerr.Precondition("legal derived-content regeneration targets a stale Block document")
			}
			if err := refreshDerivedContent(ctx, store, descriptor, command.ID, snapshot, root.SourceLocale, time.Now().UTC()); err != nil {
				return "", err
			}
			return snapshot.SnapshotDigest, store.RequestOG(ctx, kind, command.ID, "", true, descriptor.entity()+"_derived_content_regenerated")
		},
		Audit: func(string) mutation.Audit { return mutation.Unchanged() },
	})
}

// readPolicy reads the version with its document after a mutation
// committed.
func (s *Service) readPolicy(ctx context.Context, kind *policyKind, id string) (Policy, error) {
	var policy Policy
	err := readLocked(ctx, s.mutations, authorizedBefore, func(ctx context.Context, store Store) error {
		var err error
		policy, err = policyWithDocument(ctx, store, kind, id)
		return err
	})
	return policy, err
}

// policyWithDocument reads the version under a share lock with its source
// locale document.
func policyWithDocument(ctx context.Context, store Store, kind *policyKind, id string) (Policy, error) {
	persisted, err := store.Version(ctx, kind.kind, id, ShareLock)
	if err != nil {
		if errors.Is(err, ErrPolicyNotFound) {
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
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, id, false)
	if err != nil {
		return Policy{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, root.SourceLocale)
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
		Status:    persisted.Status,
		CreatedAt: persisted.CreatedAt, UpdatedAt: persisted.UpdatedAt,
		EffectiveFrom: persisted.EffectiveFrom, EffectiveUntil: persisted.EffectiveUntil,
	}, nil
}

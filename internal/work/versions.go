package work

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// ListWorkVersionsQuery lists a Work's version checkpoints, newest first.
type ListWorkVersionsQuery struct {
	WorkID string
	Page   Page
}

// RestoreWorkVersionCommand restores a Work's source content to a version.
type RestoreWorkVersionCommand struct{ WorkID, VersionID string }

// VersionContributor is a Member a version credits.
type VersionContributor struct {
	MemberID string
	Nickname string
}

// VersionView is a version checkpoint as the manage API lists it.
type VersionView struct {
	ID            string
	Version       int32
	SourceLocale  string
	Title         string
	Summary       *string
	CanonicalHash string
	CreatedAt     time.Time
	Contributors  []VersionContributor
}

// VersionPage is one page of version checkpoints.
type VersionPage struct {
	Versions   []VersionView
	Pagination Pagination
}

func (s *Service) ListWorkVersions(ctx context.Context, query ListWorkVersionsQuery) (VersionPage, error) {
	root, err := s.findWork(ctx, query.WorkID)
	if err != nil {
		return VersionPage{}, err
	}
	if err := requireWorkPermission(ctx, s.spiceDB, root, policyv1.Work.View, workAuthorizationRead); err != nil {
		return VersionPage{}, err
	}
	limit, offset := int32(20), query.Page.Offset
	if query.Page.Limit > 0 {
		limit = query.Page.Limit
	}
	var total int64
	var versions []Version
	if err := s.read(ctx, func(store Store) error {
		var err error
		total, err = store.CountVersions(ctx, query.WorkID)
		return err
	}); err != nil {
		return VersionPage{}, err
	}
	if err := s.read(ctx, func(store Store) error {
		var err error
		versions, err = store.ListVersions(ctx, query.WorkID, limit, offset)
		return err
	}); err != nil {
		return VersionPage{}, err
	}
	contributorLists := make([][]string, len(versions))
	for index := range versions {
		contributorLists[index] = versions[index].ContributorMemberIDs
	}
	var contributorNames map[string]string
	if err := s.read(ctx, func(store Store) error {
		var err error
		contributorNames, err = resolveVersionContributorNames(ctx, store, contributorLists...)
		return err
	}); err != nil {
		return VersionPage{}, err
	}
	views := make([]VersionView, len(versions))
	for index := range versions {
		views[index], err = workVersionView(&versions[index], contributorNames)
		if err != nil {
			return VersionPage{}, domainerr.Precondition("Work Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err))
		}
	}
	return VersionPage{
		Versions: views,
		Pagination: Pagination{
			Total: int32(total), Limit: limit, Offset: offset,
			HasMore: int64(offset)+int64(limit) < total,
		},
	}, nil
}

// RestoreWorkVersion replaces the full source-owned graph and locale metadata
// from one immutable checkpoint. Existing target locale rows and translation
// jobs remain unchanged, including when the checkpoint switches source-locale
// authority. Global Work fields remain current.
func (s *Service) RestoreWorkVersion(ctx context.Context, command RestoreWorkVersionCommand) (View, error) {
	version, err := s.loadWorkVersionForRestore(ctx, command.VersionID, command.WorkID)
	if err != nil {
		return View{}, err
	}
	envelope, document, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return View{}, domainerr.Precondition("Work Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err))
	}
	if envelope.Title == nil || strings.TrimSpace(*envelope.Title) == "" {
		return View{}, domainerr.Precondition("Work Version source title is invalid")
	}
	now := time.Now().UTC()
	var restore VersionRestore
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[versionrestore.Outcome, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			var err error
			restore, err = lockWorkVersionRestore(ctx, store, require, version.WorkID, envelope, document, now)
			return err
		}),
		Change: func(ctx context.Context, store Store) (versionrestore.Outcome, error) {
			outcome, err := store.RestoreVersion(ctx, restore)
			if err != nil || !outcome.Changed() {
				return outcome, err
			}
			return outcome, store.TouchWork(ctx, version.WorkID, now)
		},
		Audit: func(outcome versionrestore.Outcome) mutation.Audit {
			if !outcome.Changed() {
				return mutation.Unchanged()
			}
			return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkVersionRestoreAuditRecord(metadata, version.WorkID, version.ID)
			})
		},
		Outbox: func(ctx context.Context, store Store, outcome versionrestore.Outcome) error {
			if !outcome.Changed() {
				return nil
			}
			return publishWorkVersionRestore(ctx, store, version, envelope.SourceLocale, outcome)
		},
	})
	if err != nil {
		return View{}, normalizeWorkContentBlockError(err)
	}
	work, err := s.loadRestoredWork(ctx, version.WorkID)
	if err != nil {
		return View{}, err
	}
	return s.workResponseWithReadyOg(ctx, work, s.getWorkFeaturedImageAsset(ctx, work.ID))
}

// lockWorkVersionRestore locks the Work root, authorizes the caller and
// returns the restore of envelope and document.
func lockWorkVersionRestore(
	ctx context.Context,
	store Store,
	require mutation.Require,
	workID string,
	envelope VersionContentSnapshot,
	document *contentv1.LocalizedRichTextDocument,
	now time.Time,
) (VersionRestore, error) {
	documentID, err := loadWorkContentDocumentID(ctx, store, workID, Unlocked)
	if err != nil {
		return VersionRestore{}, err
	}
	work, err := lockWorkForContentDocument(ctx, store, documentID)
	if err != nil {
		return VersionRestore{}, err
	}
	if work.ID != workID {
		return VersionRestore{}, domainerr.Precondition("Work content document changed during version restore")
	}
	if _, err := requireLockedWorkPermission(ctx, store, require, workID, policyv1.Work.Edit, workAuthorizationMutation); err != nil {
		return VersionRestore{}, err
	}
	return VersionRestore{
		DocumentID: documentID,
		Version:    versionrestore.Source{Locale: envelope.SourceLocale, Title: envelope.Title, Summary: envelope.Summary},
		Fence:      lockedWorkContentFence(),
		LoadSource: func(ctx context.Context, store Store) (versionrestore.Source, error) {
			source, err := LoadRequiredSourceLocaleMetadata(ctx, store, workID)
			if err != nil {
				return versionrestore.Source{}, err
			}
			return versionrestore.Source{Locale: source.Locale, Title: source.Title, Summary: source.Summary}, nil
		},
		SwitchSourceLocale: func(ctx context.Context, store Store, from string, revision uuid.UUID) error {
			return switchWorkVersionRestoreSourceLocale(ctx, store, workID, envelope.SourceLocale, revision, from, now)
		},
		Replacement: func(ctx context.Context, store Store, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			unavailable, err := store.UnavailableAttachmentKinds(ctx, document)
			if err != nil {
				return contentblock.ReplaceInput{}, err
			}
			return contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
				documentID, revision, document, unavailable,
			)
		},
		UpdateMetadata: func(ctx context.Context, store Store) error {
			return updateWorkVersionSourceMetadata(ctx, store, workID, envelope.SourceLocale, envelope.Title, envelope.Summary, now)
		},
	}, nil
}

// publishWorkVersionRestore requests the OG image and signals a restore
// that changed the Work.
func publishWorkVersionRestore(
	ctx context.Context,
	store Store,
	version Version,
	sourceLocale string,
	outcome versionrestore.Outcome,
) error {
	if outcome.RefreshesOG() {
		if _, err := requestWorkOG(ctx, store, version.WorkID, sourceLocale, "work_version_restored"); err != nil {
			return err
		}
	}
	return store.PublishContentUpdate(ctx, ContentUpdate{WorkID: version.WorkID, Kind: LocaleUpdate, Locale: LocaleChange{
		Fields: outcome.Fields(), DocumentRevision: outcome.DocumentRevision.String(),
		Contributors: versionrestore.Contributors(ctx), Source: SourceManage, Locale: sourceLocale,
		DocumentStateChanged: true,
	}})
}

func (s *Service) loadWorkVersionForRestore(ctx context.Context, versionID string, expectedWorkID string) (Version, error) {
	var version Version
	err := s.read(ctx, func(store Store) error {
		var err error
		version, err = store.FindVersion(ctx, versionID)
		return err
	})
	if errors.Is(err, ErrVersionNotFound) {
		return Version{}, domainerr.ResourceNotFound("work_version", versionID)
	}
	if err != nil {
		return Version{}, err
	}
	if expectedWorkID != "" && version.WorkID != expectedWorkID {
		return Version{}, domainerr.ResourceNotFound("work_version", versionID)
	}
	return version, nil
}

func updateWorkVersionSourceMetadata(
	ctx context.Context,
	store Store,
	workID string,
	sourceLocale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	err := store.RestoreLocaleText(ctx, workID, sourceLocale, title, summary, now)
	if errors.Is(err, ErrRowChanged) {
		return domainerr.Precondition("Work source locale changed during version restore")
	}
	return err
}

func (s *Service) loadRestoredWork(ctx context.Context, workID string) (View, error) {
	var work Work
	var source LocaleMetadata
	var documents map[string]SourceDocument
	if err := s.read(ctx, func(store Store) error {
		var err error
		work, err = store.FindWork(ctx, workID)
		return err
	}); err != nil {
		return View{}, err
	}
	if err := s.read(ctx, func(store Store) error {
		var err error
		source, err = LoadRequiredSourceLocaleMetadata(ctx, store, work.ID)
		return err
	}); err != nil {
		return View{}, err
	}
	if source.Title == nil {
		return View{}, domainerr.Precondition("Work source title is not initialized")
	}
	view := View{Work: work, Title: *source.Title, Summary: cloneOptionalString(source.Summary)}
	documents, err := s.loadSourceDocuments(ctx, []string{work.ID})
	if err != nil {
		return View{}, err
	}
	if document, ok := documents[work.ID]; ok {
		view.OgAssetID = document.OgAssetID
	}
	return view, nil
}

func workVersionView(version *Version, contributorNames map[string]string) (VersionView, error) {
	if version == nil {
		return VersionView{}, errors.New("work Version is required")
	}
	envelope, _, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return VersionView{}, err
	}
	if envelope.Title == nil || strings.TrimSpace(*envelope.Title) == "" {
		return VersionView{}, errors.New("work Version source title is invalid")
	}
	digest := sha256.Sum256(version.ContentSnapshot)
	canonicalHash := fmt.Sprintf("%x", digest[:])
	contributors, err := versionContributors(version.ContributorMemberIDs, contributorNames)
	if err != nil {
		return VersionView{}, err
	}
	return VersionView{
		ID:            version.ID,
		Version:       version.Version,
		SourceLocale:  envelope.SourceLocale,
		Title:         *envelope.Title,
		Summary:       cloneOptionalString(envelope.Summary),
		CanonicalHash: canonicalHash,
		CreatedAt:     version.CreatedAt,
		Contributors:  contributors,
	}, nil
}

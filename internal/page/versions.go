package page

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type ListPageVersionsQuery struct {
	PageID string
	Page   Paging
}

// RestorePageVersionCommand restores VersionID. A non-empty PageID must own
// the version.
type RestorePageVersionCommand struct {
	PageID    string
	VersionID string
}

// VersionView is a Page version checkpoint as the manage API lists it.
type VersionView struct {
	Version       Version
	SourceLocale  string
	CanonicalHash string
	Contributors  []Contributor
}

// VersionList is one page of a Page's version checkpoints.
type VersionList struct {
	Versions   []VersionView
	Pagination Pagination
}

// ListPageVersions returns a page of the version checkpoints of a Page the
// caller may view.
func (s *Service) ListPageVersions(ctx context.Context, query ListPageVersionsQuery) (_ VersionList, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.PageID))

	if err := requirePagePermission(ctx, s.spiceDB, query.PageID, policyv1.Page.View); err != nil {
		return VersionList{}, err
	}
	limit, offset := query.Page.limitOr(20), query.Page.Offset

	var versions []Version
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		versions, total, err = store.ListVersions(ctx, query.PageID, limit, offset)
		return err
	}); err != nil {
		return VersionList{}, err
	}

	contributorLists := make([][]string, len(versions))
	for i := range versions {
		contributorLists[i] = versions[i].ContributorMemberIDs
	}
	contributorNames, err := s.resolveVersionContributorNames(ctx, contributorLists...)
	if err != nil {
		return VersionList{}, err
	}
	views := make([]VersionView, len(versions))
	for i := range versions {
		if views[i], err = versionView(versions[i], contributorNames); err != nil {
			return VersionList{}, err
		}
	}
	return VersionList{Versions: views, Pagination: Pagination{
		Total: int32(total), Limit: limit, Offset: offset, HasMore: int64(offset)+int64(limit) < total,
	}}, nil
}

// RestorePageVersion restores only the versioned source-locale title, summary,
// collaborative body, and shared document structure. Slug, visibility,
// document layout, and every other Page setting remain current.
func (s *Service) RestorePageVersion(ctx context.Context, command RestorePageVersionCommand) (_ Detail, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID), slog.String("version_id", command.VersionID))

	var version Version
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		version, err = store.FindVersion(ctx, command.VersionID)
		return err
	}); err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			return Detail{}, domainerr.ResourceNotFound("page_version", command.VersionID)
		}
		return Detail{}, err
	}
	if command.PageID != "" && version.PageID != command.PageID {
		return Detail{}, domainerr.ResourceNotFound("page_version", command.VersionID)
	}
	snapshot, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return Detail{}, failurecause.WithAttrs(
			domainerr.Precondition("Page version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
			slog.String("page_version_id", version.ID),
		)
	}

	now := time.Now().UTC()
	var documentID uuid.UUID
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[versionrestore.Outcome, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			var err error
			if _, documentID, err = loadPageAIDocumentRoot(ctx, store, version.PageID, UpdateLock); err != nil {
				return err
			}
			return requireLockedPagePermission(ctx, store, require, version.PageID, policyv1.Page.Edit)
		}),
		Change: func(ctx context.Context, store Store) (versionrestore.Outcome, error) {
			outcome, err := restorePageVersion(ctx, store, version.PageID, documentID, snapshot, now)
			if err != nil || !outcome.Changed() {
				return outcome, err
			}
			return outcome, touchRestoredPage(ctx, store, version.PageID, snapshot.SourceLocale, outcome, now)
		},
		Audit: func(outcome versionrestore.Outcome) mutation.Audit {
			if !outcome.Changed() {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageVersionRestoreAuditRecord(metadata, version.PageID, version.ID)
			})
		},
		Outbox: func(ctx context.Context, store Store, outcome versionrestore.Outcome) error {
			if !outcome.Changed() {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: version.PageID, Kind: LocaleUpdate, Locale: LocaleUpdateChange{
				Fields: outcome.Fields(), DocumentRevision: outcome.DocumentRevision.String(),
				Contributors: versionrestore.Contributors(ctx), Source: SourceManage, Locale: snapshot.SourceLocale,
				DocumentStateChanged: true,
			}})
		},
	}); err != nil {
		return Detail{}, normalizePageContentBlockError(err)
	}
	return s.pageBy(ctx, ByID, version.PageID)
}

func restorePageVersion(
	ctx context.Context,
	store Store,
	pageID string,
	documentID uuid.UUID,
	snapshot VersionContentSnapshot,
	now time.Time,
) (versionrestore.Outcome, error) {
	return versionrestore.Restore[Store]{
		Store:      versionRestoreContent{store: store, fence: lockedPageContentFence(pageID)},
		DocumentID: documentID,
		Version:    versionrestore.Source{Locale: snapshot.SourceLocale, Title: snapshot.Title, Summary: snapshot.Summary},
		LoadSource: func(ctx context.Context, store Store) (versionrestore.Source, error) {
			source, err := loadRequiredPageSourceLocaleMetadata(ctx, store, pageID)
			return versionrestore.Source{Locale: source.Locale, Title: source.Title, Summary: source.Summary}, err
		},
		SwitchSourceLocale: func(ctx context.Context, store Store, from string, revision uuid.UUID) error {
			return switchVersionRestoreSourceLocale(ctx, store, pageID, snapshot.SourceLocale, revision, from, now)
		},
		Replacement: func(ctx context.Context, store Store, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			unavailable, err := store.UnavailableAttachments(ctx, snapshot.Document)
			if err != nil {
				return contentblock.ReplaceInput{}, err
			}
			return contentblock.ReplaceFromLocalizedPageProtoWithUnavailableAttachments(documentID, revision, snapshot.Document, unavailable)
		},
		UpdateMetadata: func(ctx context.Context, store Store) error {
			return updatePageVersionSourceMetadata(ctx, store, pageID, snapshot.SourceLocale, snapshot.Title, snapshot.Summary, now)
		},
	}.Apply(ctx, store)
}

// switchVersionRestoreSourceLocale changes only the Page source-locale
// pointer before restoring the immutable Version. Existing locale values,
// Block overlays, and Translation jobs remain unchanged.
func switchVersionRestoreSourceLocale(
	ctx context.Context,
	store Store,
	pageID string,
	requestedLocale string,
	expectedDocumentRevision uuid.UUID,
	previousLocale string,
	now time.Time,
) error {
	if strings.TrimSpace(pageID) == "" ||
		strings.TrimSpace(previousLocale) == "" || strings.TrimSpace(requestedLocale) == "" ||
		expectedDocumentRevision == uuid.Nil {
		return domainerr.InvalidField("source_locale", "source-locale switch identity is required")
	}
	if previousLocale == requestedLocale {
		return domainerr.Precondition("requested locale is already the source locale")
	}
	root, err := store.DocumentRoot(ctx, pageID, Unlocked)
	if err != nil {
		return err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return domainerr.Precondition("Page content document is not initialized")
	}
	if root.SourceLocale != previousLocale {
		return translation.ErrSourceNoLongerCurrent
	}
	_, err = store.AdvanceContentRevision(ctx,
		contentblock.AdvanceInput{DocumentID: *root.ContentDocumentID, ExpectedRevision: expectedDocumentRevision},
		func(context.Context, Store, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: previousLocale}, nil
		},
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			switched, err := store.SwitchSourceLocale(ctx, pageID, previousLocale, requestedLocale, now)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if !switched {
				return contentblock.MetadataEffect{}, translation.ErrSourceNoLongerCurrent
			}
			return contentblock.MetadataEffect{
				Changed:                  true,
				AffectsTranslationSource: true,
				SourceLocale:             requestedLocale,
			}, nil
		},
	)
	return err
}

// touchRestoredPage records a restore that changed the Page on its root and
// requests the OG image of a changed title.
func touchRestoredPage(
	ctx context.Context,
	store Store,
	pageID string,
	sourceLocale string,
	outcome versionrestore.Outcome,
	now time.Time,
) error {
	if err := store.TouchPage(ctx, pageID, now); err != nil {
		return err
	}
	if !outcome.RefreshesOG() {
		return nil
	}
	_, err := store.RequestOG(ctx, pageID, sourceLocale, false, "page_version_restored")
	return err
}

func updatePageVersionSourceMetadata(
	ctx context.Context,
	store Store,
	pageID string,
	sourceLocale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	exists, err := store.UpdateLocale(ctx, pageID, sourceLocale, LocaleChange{
		SetTitle: true, Title: title, SetSummary: true, Summary: summary, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	if !exists {
		return domainerr.Precondition("Page source locale changed during version restore")
	}
	return nil
}

func versionView(v Version, contributorNames map[string]string) (VersionView, error) {
	snapshot, err := DecodeVersionContentSnapshot(v.ContentSnapshot)
	if err != nil {
		return VersionView{}, failurecause.WithAttrs(
			domainerr.Precondition("Page version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
			slog.String("page_version_id", v.ID),
		)
	}
	contributors, err := versionContributors(v.ContributorMemberIDs, contributorNames)
	if err != nil {
		return VersionView{}, err
	}
	return VersionView{
		Version: v, SourceLocale: snapshot.SourceLocale,
		CanonicalHash: fmt.Sprintf("%x", sha256.Sum256(v.ContentSnapshot)), Contributors: contributors,
	}, nil
}

package post

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
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

// switchPostVersionRestoreSourceLocale moves source-locale authority and
// advances the shared document CAS in the same transaction. The post root is
// already locked by RestorePostVersion, so the callback only rechecks the
// expected old locale before changing the owning row.
func switchPostVersionRestoreSourceLocale(
	ctx context.Context,
	store Store,
	postID string,
	documentID uuid.UUID,
	previousLocale string,
	nextLocale string,
	expectedRevision uuid.UUID,
	now time.Time,
) error {
	if strings.TrimSpace(previousLocale) == "" || strings.TrimSpace(nextLocale) == "" ||
		previousLocale == nextLocale {
		return domainerr.InvalidField("source_locale", "source-locale switch identity is invalid")
	}
	_, err := store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		func(context.Context, Store, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: previousLocale}, nil
		},
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			updated, err := store.SwitchSourceLocale(ctx, postID, previousLocale, nextLocale, now)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if updated != 1 {
				return contentblock.MetadataEffect{}, domainerr.Precondition("Post source locale changed; reload before restoring")
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true, SourceLocale: nextLocale,
				ChangedLocales: []string{nextLocale},
			}, nil
		},
	)
	return err
}

// ListPostVersionsQuery lists a Post's version checkpoints, newest first.
type ListPostVersionsQuery struct {
	PostID string
	Page   Page
}

// RestorePostVersionCommand restores a Post's source content to a version.
type RestorePostVersionCommand struct{ PostID, VersionID string }

func (s *Service) ListPostVersions(ctx context.Context, query ListPostVersionsQuery) (_ VersionPage, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	limit, offset := query.Page.limitOr(20), query.Page.Offset
	var total int64
	var versions []StoredVersion
	access := mutation.Checked(func(ctx context.Context, store Store) error {
		status, err := store.KeySharePostStatus(ctx, query.PostID)
		if err != nil {
			if errors.Is(err, ErrPostNotFound) {
				return domainerr.ResourceNotFound("post", query.PostID)
			}
			return err
		}
		_, err = requirePostViewForStatus(ctx, s.spiceDB, query.PostID, status)
		return err
	})
	if err := s.readLocked(ctx, access, func(_ context.Context, store Store) error {
		var err error
		versions, total, err = store.ListVersions(ctx, query.PostID, limit, offset)
		return err
	}); err != nil {
		return VersionPage{}, err
	}
	contributorLists := make([][]string, len(versions))
	for i := range versions {
		contributorLists[i] = versions[i].ContributorMemberIDs
	}
	contributorNames, err := s.resolveVersionContributorNames(ctx, contributorLists...)
	if err != nil {
		return VersionPage{}, err
	}
	result := make([]Version, len(versions))
	for i := range versions {
		result[i], err = postVersion(versions[i], contributorNames)
		if err != nil {
			return VersionPage{}, failurecause.WithAttrs(
				domainerr.Precondition("Post Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
				slog.String("post_version_id", versions[i].ID),
			)
		}
	}
	return VersionPage{
		Versions: result,
		Pagination: Pagination{
			Total: int32(total), Limit: limit, Offset: offset, HasMore: int64(offset)+int64(limit) < total,
		},
	}, nil
}

// RestorePostVersion replaces the full source-owned graph and source overlay.
// A snapshot locale change switches source authority without rewriting stored
// locale rows or already requested translation jobs.
// Category and Tag remain current document metadata and are not versioned.
func (s *Service) RestorePostVersion(ctx context.Context, command RestorePostVersionCommand) (_ View, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID), slog.String("version_id", command.VersionID))

	version, err := readStore(ctx, s, func(store Store) (StoredVersion, error) { return store.FindVersion(ctx, command.VersionID) })
	if err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			return View{}, domainerr.ResourceNotFound("post_version", command.VersionID)
		}
		return View{}, err
	}
	if command.PostID != "" && version.PostID != command.PostID {
		return View{}, domainerr.ResourceNotFound("post_version", command.VersionID)
	}
	envelope, document, err := unmarshalPostVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return View{}, failurecause.WithAttrs(
			domainerr.Precondition("Post Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
			slog.String("post_version_id", version.ID),
		)
	}
	now := time.Now().UTC()
	var restore versionrestore.Restore[Store]
	_, _, err = runPostMutation(ctx, s, mutation.StoreSpec[struct{}, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			var err error
			restore, err = lockPostVersionRestore(ctx, store, require, version.PostID, envelope, document, now)
			return err
		}),
	}, func(ctx context.Context, store Store, effects *postEffects) (struct{}, error) {
		outcome, err := restore.Apply(ctx, store)
		if err != nil || !outcome.Changed() {
			return struct{}{}, err
		}
		return struct{}{}, recordPostVersionRestore(ctx, store, effects, version, envelope.SourceLocale, outcome, now)
	})
	if err != nil {
		return View{}, normalizePostContentBlockError(err)
	}
	restored, err := readStore(ctx, s, func(store Store) (Post, error) { return store.FindPost(ctx, version.PostID) })
	if err != nil {
		return View{}, err
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &restored); err != nil {
		return View{}, err
	}
	return s.postView(ctx, restored)
}

// lockPostVersionRestore locks the Post root, authorizes the caller and
// returns the restore of envelope and document.
func lockPostVersionRestore(
	ctx context.Context,
	store Store,
	require mutation.Require,
	postID string,
	envelope postVersionContentSnapshot,
	document *contentv1.LocalizedRichTextDocument,
	now time.Time,
) (versionrestore.Restore[Store], error) {
	documentID, err := loadPostContentDocumentID(ctx, store, postID)
	if err != nil {
		return versionrestore.Restore[Store]{}, err
	}
	root, err := lockPostContentDocumentRoot(ctx, store, postID, documentID, true)
	if err != nil {
		return versionrestore.Restore[Store]{}, err
	}
	if _, err := requireLockedPostActionForStatus(require, postID, root.Status, policyv1.Post.Manage); err != nil {
		return versionrestore.Restore[Store]{}, err
	}
	return versionrestore.Restore[Store]{
		DocumentID: documentID,
		Version:    versionrestore.Source{Locale: envelope.SourceLocale, Title: envelope.Title, Summary: envelope.Summary},
		Store:      versionRestoreContent{store: store, fence: postLockedDocumentFence(postID, true)},
		LoadSource: func(ctx context.Context, store Store) (versionrestore.Source, error) {
			domain, err := loadPostContentDomainContext(ctx, store, postID)
			if err != nil {
				return versionrestore.Source{}, err
			}
			metadata, err := loadPostLocaleMetadataRow(ctx, store, postID, domain.SourceLocale, false)
			if err != nil {
				return versionrestore.Source{}, err
			}
			return versionrestore.Source{Locale: domain.SourceLocale, Title: metadata.Title, Summary: metadata.Summary}, nil
		},
		SwitchSourceLocale: func(ctx context.Context, store Store, from string, revision uuid.UUID) error {
			return switchPostVersionRestoreSourceLocale(
				ctx, store, postID, documentID, from, envelope.SourceLocale, revision, now,
			)
		},
		Replacement: func(ctx context.Context, store Store, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			unavailable, err := store.UnavailableVersionAttachments(ctx, document)
			if err != nil {
				return contentblock.ReplaceInput{}, err
			}
			return contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
				documentID, revision, document, unavailable,
			)
		},
		UpdateMetadata: func(ctx context.Context, store Store) error {
			return updatePostVersionSourceMetadata(
				ctx, store, postID, envelope.SourceLocale, envelope.Title, envelope.Summary, now,
			)
		},
	}, nil
}

// recordPostVersionRestore touches a Post a restore changed and names its
// record, OG request and signal.
func recordPostVersionRestore(
	ctx context.Context,
	store Store,
	effects *postEffects,
	version StoredVersion,
	sourceLocale string,
	outcome versionrestore.Outcome,
	now time.Time,
) error {
	if err := store.TouchPost(ctx, version.PostID, now); err != nil {
		return err
	}
	if outcome.RefreshesOG() {
		effects.requestOG(version.PostID, sourceLocale, false, "post_version_restored")
	}
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostVersionRestoreAuditRecord(metadata, version.PostID, version.ID)
	})
	effects.signal(ContentUpdate{
		PostID: version.PostID, Fields: outcome.Fields(),
		DocumentRevision: outcome.DocumentRevision.String(), Contributors: versionrestore.Contributors(ctx),
		Source: SourceManage, Locale: sourceLocale,
		DocumentStateChanged: true,
	})
	return nil
}

func updatePostVersionSourceMetadata(
	ctx context.Context,
	store Store,
	postID string,
	locale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	updated, err := store.UpdateLocaleMetadata(ctx, postID, locale, LocaleChange{
		SetTitle: true, Title: title, SetSummary: true, Summary: summary, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return domainerr.Precondition("Post source locale metadata is not initialized")
	}
	return nil
}

func postVersion(version StoredVersion, contributorNames map[string]string) (Version, error) {
	envelope, _, err := unmarshalPostVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return Version{}, err
	}
	contributors, err := versionContributors(version.ContributorMemberIDs, contributorNames)
	if err != nil {
		return Version{}, err
	}
	digest := sha256.Sum256(version.ContentSnapshot)
	result := Version{
		ID: version.ID, Version: version.Version,
		SourceLocale:  envelope.SourceLocale,
		CanonicalHash: fmt.Sprintf("%x", digest[:]),
		CreatedAt:     version.CreatedAt, Contributors: contributors,
	}
	if envelope.Title != nil {
		result.Title = *envelope.Title
	}
	result.Summary = cloneNullableString(envelope.Summary)
	return result, nil
}

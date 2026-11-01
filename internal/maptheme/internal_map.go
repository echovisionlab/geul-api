package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// requireLockedMapThemeContributorsEdit is the final collaboration mutation
// fence. The Collab service is authenticated separately, but its contributor
// IDs are only an attribution claim: each is re-resolved to a canonical live
// Member/Identity while the Map Theme root is locked, then checked against the
// generated Map Theme edit permission with fully-consistent SpiceDB semantics.
func (s *Service) requireLockedMapThemeContributorsEdit(
	ctx context.Context,
	store Store,
	themeID string,
	memberIDs []string,
) error {
	denied := domainerr.Denied("no permission to edit this map theme")
	if len(memberIDs) != 1 {
		return denied
	}
	memberID := memberIDs[0]
	if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
		return domainerr.InvalidField("contributor_member_ids", "must contain exactly one canonical Member UUID")
	}
	can, err := policyv1.MapTheme.Edit(themeID)
	if err != nil {
		return domainerr.ResourceNotFound("theme", themeID).WithCause(err)
	}
	identityID, err := store.LockContributor(ctx, memberID)
	if err != nil {
		if authorizationtarget.FailedToDecide(err) {
			return err
		}
		return denied
	}
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return denied.WithCause(err)
	}
	allowed, err := s.spiceDB.CheckActorCan(ctx, actor, can)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return denied
	}
	return nil
}

// SaveCollaborationSnapshotCommand stores the snapshot the collaboration
// service materialized from a document read at ExpectedRevision.
type SaveCollaborationSnapshotCommand struct {
	ThemeID              string
	Locale               string
	ExpectedRevision     int64
	Snapshot             *SnapshotInput
	ContributorMemberIDs []string
}

// LoadCollaborationSnapshotQuery names the theme a collaboration document
// opens.
type LoadCollaborationSnapshotQuery struct {
	ThemeID string
	Locale  string
}

// CollaborationSnapshot is the stored snapshot of a theme and its revision.
type CollaborationSnapshot struct {
	Snapshot Snapshot
	Revision int64
}

// SaveCollaborationSnapshot stores a collaboration snapshot and returns the
// new revision. The theme is locked again at the final save boundary: it can
// be deleted or its revision can change while a document is open, and no
// snapshot may be accepted for a deleted theme.
func (s *Service) SaveCollaborationSnapshot(ctx context.Context, command SaveCollaborationSnapshotCommand) (int64, error) {
	themeID, err := normalizeMapThemeID(command.ThemeID, "theme_id")
	if err != nil {
		return 0, err
	}
	if command.ExpectedRevision <= 0 {
		return 0, domainerr.InvalidField("expected_revision", "must be positive")
	}
	if command.Locale != "und" {
		return 0, domainerr.InvalidField("locale", "Map Theme locale must be und")
	}
	snapshot, err := command.Snapshot.snapshot("snapshot.")
	if err != nil {
		return 0, err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			current, err := store.LockTheme(ctx, themeID)
			if err != nil {
				return err
			}
			if current.Revision != command.ExpectedRevision {
				return mapThemeChanged(fmt.Sprint(current.Revision))
			}
			return nil
		},
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return s.requireLockedMapThemeContributorsEdit(ctx, store, themeID, command.ContributorMemberIDs)
		}),
		Change: func(ctx context.Context, store Store) (string, error) {
			return command.ContributorMemberIDs[0], writeSnapshot(ctx, store, themeID, command.ExpectedRevision, *snapshot, command.ExpectedRevision)
		},
		Audit: func(contributor string) mutation.Audit {
			return mutation.MemberAudit(contributor, sharedtelemetry.AuditMapThemeUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeContentUpdatedAuditRecord(metadata, themeID)
			})
		},
	})
	if stderrors.Is(err, ErrThemeNotFound) {
		return 0, domainerr.ResourceNotFound("map_theme", themeID)
	}
	if err != nil {
		return 0, err
	}
	return command.ExpectedRevision + 1, nil
}

func (s *Service) LoadCollaborationSnapshot(ctx context.Context, query LoadCollaborationSnapshotQuery) (CollaborationSnapshot, error) {
	themeID, err := normalizeMapThemeID(query.ThemeID, "theme_id")
	if err != nil {
		return CollaborationSnapshot{}, err
	}
	if query.Locale != "und" {
		return CollaborationSnapshot{}, domainerr.InvalidField("locale", "Map Theme locale must be und")
	}
	theme, err := s.readTheme(ctx, themeID)
	if err != nil {
		if stderrors.Is(err, ErrThemeNotFound) {
			return CollaborationSnapshot{}, domainerr.ResourceNotFound("map_theme", themeID)
		}
		return CollaborationSnapshot{}, err
	}
	return CollaborationSnapshot{Snapshot: theme.Snapshot, Revision: theme.Revision}, nil
}

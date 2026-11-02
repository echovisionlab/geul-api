package artist

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/google/uuid"
)

// artistDocumentMetadataPlan is a parsed Artist document-metadata update:
// the columns Artist shares with Label, its real name and its Labels.
type artistDocumentMetadataPlan struct {
	columns     entityparticipant.MetadataColumns
	realName    entityparticipant.NullableStringUpdate
	labelIDs    []string
	setLabelIDs bool
}

// DocumentMetadataUpdate is a collaboration room's Artist metadata update.
// A nil mutation leaves its field alone; SocialLinksPresent replaces the
// social links and LabelIDsPresent the Labels.
type DocumentMetadataUpdate struct {
	RealName           *entityparticipant.StringMutation
	CountryCode        *entityparticipant.StringMutation
	Website            *entityparticipant.StringMutation
	Slug               *entityparticipant.StringMutation
	ParentArtistID     *entityparticipant.StringMutation
	SocialLinksPresent bool
	SocialLinks        map[string]string
	LabelIDsPresent    bool
	LabelIDs           []string
}

func buildArtistDocumentMetadataPlan(input *DocumentMetadataUpdate) (artistDocumentMetadataPlan, error) {
	if input == nil {
		return artistDocumentMetadataPlan{}, domainerr.Required("update")
	}
	var plan artistDocumentMetadataPlan
	var err error
	if plan.realName, err = entityparticipant.ParseNullableStringUpdate("real_name", input.RealName); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.CountryCode, err = entityparticipant.ParseNullableStringUpdate("country_code", input.CountryCode); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.Website, err = entityparticipant.ParseNullableStringUpdate("website", input.Website); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.Slug, err = entityparticipant.ParseNullableStringUpdate("slug", input.Slug); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.Parent, err = entityparticipant.ParseNullableStringUpdate("parent_artist_id", input.ParentArtistID); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if err := artistEntity.Complete(&plan.columns, input.SocialLinksPresent, input.SocialLinks); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if input.LabelIDsPresent {
		plan.labelIDs, err = normalizeArtistLabelIDs(input.LabelIDs)
		if err != nil {
			return artistDocumentMetadataPlan{}, err
		}
		plan.setLabelIDs = true
	}
	if !plan.realName.Present && !plan.columns.Present() && !plan.setLabelIDs {
		return artistDocumentMetadataPlan{}, domainerr.InvalidField("update", "at least one metadata value is required")
	}
	return plan, nil
}

func normalizeArtistLabelIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, domainerr.InvalidField("label_ids", "must contain valid Label IDs")
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			return nil, domainerr.InvalidField("label_ids", "must contain unique Label IDs")
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

// validateArtistLabelIDsExist requires every Label of labelIDs to exist.
func validateArtistLabelIDsExist(ctx context.Context, store Store, labelIDs []string) error {
	if len(labelIDs) == 0 {
		return nil
	}
	existing, err := store.LabelTitles(ctx, labelIDs)
	if err != nil {
		return err
	}
	if len(existing) != len(labelIDs) {
		return domainerr.InvalidField("label_ids", "contains a Label that does not exist")
	}
	return nil
}

// UpdateArtistDocumentMetadataCommand saves the Artist's document metadata
// from its source-locale collaboration room.
type UpdateArtistDocumentMetadataCommand struct {
	ArtistID             string
	Locale               string
	ExpectedRevision     string
	Update               *DocumentMetadataUpdate
	ContributorMemberIDs []string
}

// MetadataUpdate is a saved document-metadata update in the source locale.
type MetadataUpdate struct {
	Locale  string
	Advance contentblock.AdvanceResult
}

// UpdateArtistDocumentMetadata saves the real name, country, website,
// social links, slug, parent Artist and Labels, moving the SpiceDB parent
// with it. Only the source-locale room may save metadata.
func (s *Service) UpdateArtistDocumentMetadata(ctx context.Context, command UpdateArtistDocumentMetadataCommand) (MetadataUpdate, error) {
	var locale string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		locale, err = requireArtistSourceRoomLocale(ctx, store, command.ArtistID, command.Locale)
		return err
	}); err != nil {
		return MetadataUpdate{}, err
	}
	plan, err := buildArtistDocumentMetadataPlan(command.Update)
	if err != nil {
		return MetadataUpdate{}, err
	}
	expectedRevision, err := uuid.Parse(command.ExpectedRevision)
	if err != nil {
		return MetadataUpdate{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	documentID, err := s.readCreativeContentDocumentID(ctx, command.ArtistID)
	if err != nil {
		return MetadataUpdate{}, err
	}
	var parent entityparticipant.ParentUpdate
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		parent, err = artistEntity.PlanParentUpdate(ctx, store, command.ArtistID, plan.columns.Parent)
		return err
	}); err != nil {
		return MetadataUpdate{}, err
	}
	fence := artistSourceRoomFence(command.ArtistID, locale)
	advanced, err := s.participants.SaveMetadata(ctx, entityparticipant.MetadataSave[Store]{
		EntityID: command.ArtistID, Parent: parent, Access: contributorAccess(command.ArtistID, command.ContributorMemberIDs),
		Save: func(ctx context.Context, store Store, writesParent bool) (contentblock.AdvanceResult, *string, error) {
			var currentParent *string
			advanced, err := store.AdvanceContentRevision(
				ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision}, fence,
				func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
					changed, parentID, err := applyArtistDocumentMetadata(ctx, store, command.ArtistID, plan, parent, writesParent)
					currentParent = parentID
					return contentblock.MetadataEffect{Changed: changed}, err
				},
			)
			if err != nil {
				return contentblock.AdvanceResult{}, currentParent, normalizeCreativeContentBlockError(err)
			}
			return advanced, currentParent, nil
		},
	})
	return MetadataUpdate{Locale: locale, Advance: advanced}, err
}

// applyArtistDocumentMetadata writes the changed metadata columns of the
// Artist row and replaces its Labels under the content fence. It reports
// whether anything changed and the parent it read under the lock.
func applyArtistDocumentMetadata(ctx context.Context, store Store, artistID string, plan artistDocumentMetadataPlan, parent entityparticipant.ParentUpdate, writesParent bool) (bool, *string, error) {
	current, err := store.DocumentMetadata(ctx, artistID)
	if err != nil {
		return false, nil, err
	}
	var change MetadataChange
	realNameChanged := plan.realName.Assign(&change.RealName, current.RealName)
	changed, err := artistEntity.AssignColumns(ctx, store, artistID, plan.columns, parent, writesParent, entityparticipant.MetadataRow{
		Slug: current.Slug, CountryCode: current.CountryCode, Website: current.Website,
		Parent: current.ParentArtistID, SocialLinks: current.SocialLinks,
	}, &change.MetadataChange, func(ctx context.Context, slug string) error {
		taken, err := store.SlugTaken(ctx, slug, artistID)
		if err != nil {
			return err
		}
		if taken {
			return domainerr.Duplicate("artist", "slug", slug)
		}
		return store.LockResourceRoute(ctx, slug)
	})
	if err != nil {
		return false, current.ParentArtistID, err
	}
	labelsChanged, err := replaceChangedArtistLabels(ctx, store, artistID, plan)
	if err != nil || (!changed && !realNameChanged && !labelsChanged) {
		return false, current.ParentArtistID, err
	}
	if !change.Empty() {
		if err := store.UpdateDocumentMetadata(ctx, artistID, change, time.Now().UTC()); err != nil {
			return false, current.ParentArtistID, err
		}
	}
	return true, current.ParentArtistID, nil
}

// replaceChangedArtistLabels replaces the Artist's Labels when the plan
// sets different ones.
func replaceChangedArtistLabels(ctx context.Context, store Store, artistID string, plan artistDocumentMetadataPlan) (bool, error) {
	currentLabelIDs, err := store.LabelIDs(ctx, artistID)
	if err != nil {
		return false, err
	}
	if !plan.setLabelIDs || slices.Equal(currentLabelIDs, plan.labelIDs) {
		return false, nil
	}
	if err := validateArtistLabelIDsExist(ctx, store, plan.labelIDs); err != nil {
		return false, err
	}
	return true, store.ReplaceLabelIDs(ctx, artistID, plan.labelIDs)
}

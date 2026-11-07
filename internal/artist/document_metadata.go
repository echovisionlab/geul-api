package artist

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/google/uuid"
)

// artistDocumentMetadataPlan is a parsed Artist document-metadata update:
// the columns Artist shares with Label and its real name.
type artistDocumentMetadataPlan struct {
	columns  entityparticipant.MetadataColumns
	realName entityparticipant.NullableStringUpdate
}

// DocumentMetadataUpdate is a collaboration room's Artist metadata update.
// A nil mutation leaves its field alone; SocialLinksPresent replaces the
// social links.
type DocumentMetadataUpdate struct {
	RealName           *entityparticipant.StringMutation
	CountryCode        *entityparticipant.StringMutation
	Website            *entityparticipant.StringMutation
	Slug               *entityparticipant.StringMutation
	SocialLinksPresent bool
	SocialLinks        map[string]string
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
	if err := artistEntity.Complete(&plan.columns, input.SocialLinksPresent, input.SocialLinks); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if !plan.realName.Present && !plan.columns.Present() {
		return artistDocumentMetadataPlan{}, domainerr.InvalidField("update", "at least one metadata value is required")
	}
	return plan, nil
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

// UpdateArtistDocumentMetadata saves the real name, country, website, social
// links and slug. Only the source-locale room may save metadata.
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
	fence := artistSourceRoomFence(command.ArtistID, locale)
	advanced, err := s.participants.SaveMetadata(ctx, entityparticipant.MetadataSave[Store]{
		Access: contributorAccess(command.ArtistID, command.ContributorMemberIDs),
		Save: func(ctx context.Context, store Store) (contentblock.AdvanceResult, error) {
			advanced, err := store.AdvanceContentRevision(
				ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision}, fence,
				func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
					changed, err := applyArtistDocumentMetadata(ctx, store, command.ArtistID, plan)
					return contentblock.MetadataEffect{Changed: changed}, err
				},
			)
			if err != nil {
				return contentblock.AdvanceResult{}, normalizeCreativeContentBlockError(err)
			}
			return advanced, nil
		},
	})
	return MetadataUpdate{Locale: locale, Advance: advanced}, err
}

// applyArtistDocumentMetadata writes the changed metadata columns of the
// Artist row under the content fence. It reports whether anything changed.
func applyArtistDocumentMetadata(ctx context.Context, store Store, artistID string, plan artistDocumentMetadataPlan) (bool, error) {
	current, err := store.DocumentMetadata(ctx, artistID)
	if err != nil {
		return false, err
	}
	var change MetadataChange
	realNameChanged := plan.realName.Assign(&change.RealName, current.RealName)
	changed, err := artistEntity.AssignColumns(ctx, plan.columns, entityparticipant.MetadataRow{
		Slug: current.Slug, CountryCode: current.CountryCode, Website: current.Website, SocialLinks: current.SocialLinks,
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
	if err != nil || (!changed && !realNameChanged) {
		return false, err
	}
	return true, store.UpdateDocumentMetadata(ctx, artistID, change, time.Now().UTC())
}

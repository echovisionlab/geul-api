package release

import (
	"context"
	"fmt"
)

// GetReleaseRelationsQuery names the Release whose relations an editor reads.
type GetReleaseRelationsQuery struct {
	ReleaseID string
}

// EditorArtist is one Artist of a Release in the relation editor.
type EditorArtist struct {
	ArtistID  string
	Name      string
	Slug      *string
	SortOrder int32
}

// EditorLabel is one Label of a Release in the relation editor.
type EditorLabel struct {
	LabelID       string
	Name          string
	Slug          *string
	CatalogNumber *string
	SortOrder     int32
}

// EditorCreditView is one credit of a Release in the relation editor, with
// its Artist's name and slug.
type EditorCreditView struct {
	EditorCredit
	ArtistName *string
	ArtistSlug *string
}

// Relations is the editable relation snapshot of a Release. Every list is
// non-nil.
type Relations struct {
	ReleaseID  string
	Artists    []EditorArtist
	Labels     []EditorLabel
	Categories []Reference
	Genres     []Reference
	Styles     []Reference
	Formats    []FormatReference
	Credits    []EditorCreditView
}

// GetReleaseRelations returns the current editable relation snapshot after checking
// the same view permission as GetRelease. It is intentionally read-only: relation
// mutations still validate write permission in their own transaction.
func (s *Service) GetReleaseRelations(ctx context.Context, query GetReleaseRelationsQuery) (Relations, error) {
	if err := requireReleaseAction(ctx, s.spiceDB, query.ReleaseID, releaseActionView); err != nil {
		return Relations{}, err
	}
	relations := Relations{ReleaseID: query.ReleaseID}
	err := s.units.Read(ctx, func(store Store) error {
		if err := store.ReleaseExists(ctx, query.ReleaseID); err != nil {
			return releaseMissingAs(err, releaseNotFound(query.ReleaseID))
		}
		for _, load := range []func(context.Context, Store, string, *Relations) error{
			loadReleaseEditorArtists, loadReleaseEditorLabels, loadReleaseEditorReferences, loadReleaseEditorCredits,
		} {
			if err := load(ctx, store, query.ReleaseID, &relations); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Relations{}, err
	}
	return relations, nil
}

// optionalSlug is the editor form of a summary slug: absent when empty.
func optionalSlug(slug string) *string {
	if slug == "" {
		return nil
	}
	return &slug
}

func loadReleaseEditorArtists(ctx context.Context, store Store, releaseID string, relations *Relations) error {
	rows, err := store.ReleaseArtists(ctx, releaseID)
	if err != nil {
		return fmt.Errorf("load release artists: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArtistID)
	}
	artists, err := store.ArtistSummaries(ctx, ids)
	if err != nil {
		return fmt.Errorf("load release artists: %w", err)
	}
	relations.Artists = make([]EditorArtist, 0, len(rows))
	for _, row := range rows {
		artist, exists := artists[row.ArtistID]
		if !exists {
			continue
		}
		relations.Artists = append(relations.Artists, EditorArtist{
			ArtistID: row.ArtistID, Name: artist.Title, Slug: optionalSlug(artist.Slug), SortOrder: int32(row.SortOrder),
		})
	}
	return nil
}

func loadReleaseEditorLabels(ctx context.Context, store Store, releaseID string, relations *Relations) error {
	rows, err := store.ReleaseLabels(ctx, releaseID)
	if err != nil {
		return fmt.Errorf("load release labels: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.LabelID)
	}
	labels, err := store.LabelSummaries(ctx, ids)
	if err != nil {
		return fmt.Errorf("load release labels: %w", err)
	}
	relations.Labels = make([]EditorLabel, 0, len(rows))
	for _, row := range rows {
		label, exists := labels[row.LabelID]
		if !exists {
			continue
		}
		relations.Labels = append(relations.Labels, EditorLabel{
			LabelID: row.LabelID, Name: label.Title, Slug: optionalSlug(label.Slug),
			CatalogNumber: row.CatalogNumber, SortOrder: int32(row.SortOrder),
		})
	}
	return nil
}

func loadReleaseEditorReferences(ctx context.Context, store Store, releaseID string, relations *Relations) error {
	for _, relation := range []struct {
		relation ReferenceRelation
		table    string
		dest     *[]Reference
	}{
		{ReferenceCategories, "release_category", &relations.Categories},
		{ReferenceGenres, "release_genre", &relations.Genres},
		{ReferenceStyles, "release_style", &relations.Styles},
	} {
		references, err := store.References(ctx, relation.relation, releaseID)
		if err != nil {
			return fmt.Errorf("load release %s: %w", relation.table, err)
		}
		*relation.dest = append(make([]Reference, 0, len(references)), references...)
	}
	formats, err := store.Formats(ctx, releaseID)
	if err != nil {
		return fmt.Errorf("load release formats: %w", err)
	}
	relations.Formats = append(make([]FormatReference, 0, len(formats)), formats...)
	return nil
}

func loadReleaseEditorCredits(ctx context.Context, store Store, releaseID string, relations *Relations) error {
	rows, err := store.EditorCredits(ctx, releaseID)
	if err != nil {
		return fmt.Errorf("load release credits: %w", err)
	}
	artistIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ArtistID != nil {
			artistIDs = append(artistIDs, *row.ArtistID)
		}
	}
	artists, err := store.ArtistSummaries(ctx, artistIDs)
	if err != nil {
		return fmt.Errorf("load release credits: %w", err)
	}
	relations.Credits = make([]EditorCreditView, 0, len(rows))
	for _, row := range rows {
		credit := EditorCreditView{EditorCredit: row}
		if row.ArtistID != nil {
			artist := artists[*row.ArtistID]
			credit.ArtistName, credit.ArtistSlug = &artist.Title, optionalSlug(artist.Slug)
		}
		relations.Credits = append(relations.Credits, credit)
	}
	return nil
}

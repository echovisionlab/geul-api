package entityparticipant

import (
	"context"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
)

// StringMutation is one document-metadata string mutation as a collaboration
// room sends it: Set a value, Clear it, or, when both are nil, name neither.
type StringMutation struct {
	Set   *string
	Clear *bool
}

// NullableStringUpdate is a parsed document-metadata string mutation. Present
// is false when the field is left alone; a nil Value clears it.
type NullableStringUpdate struct {
	Present bool
	Value   *string
}

// ParseNullableStringUpdate trims a set value and rejects an empty set, a
// false clear and an empty mutation. A nil input leaves the field alone.
func ParseNullableStringUpdate(field string, input *StringMutation) (NullableStringUpdate, error) {
	if input == nil {
		return NullableStringUpdate{}, nil
	}
	switch {
	case input.Set != nil:
		value := strings.TrimSpace(*input.Set)
		if value == "" {
			return NullableStringUpdate{}, domainerr.InvalidField(field, "set value must not be empty; use clear instead")
		}
		return NullableStringUpdate{Present: true, Value: &value}, nil
	case input.Clear != nil:
		if !*input.Clear {
			return NullableStringUpdate{}, domainerr.InvalidField(field, "clear must be true")
		}
		return NullableStringUpdate{Present: true}, nil
	default:
		return NullableStringUpdate{}, domainerr.InvalidField(field, "set or clear is required")
	}
}

// Changes reports whether the update gives current a different value.
func (u NullableStringUpdate) Changes(current *string) bool {
	return u.Present && !sameOptionalString(current, u.Value)
}

// Assignment is the new value of one nullable column a save changes; Value
// nil clears it.
type Assignment struct {
	Changed bool
	Value   *string
}

// Assign records the update in target when it changes current.
func (u NullableStringUpdate) Assign(target *Assignment, current *string) bool {
	if !u.Changes(current) {
		return false
	}
	*target = Assignment{Changed: true, Value: u.Value}
	return true
}

// MetadataColumns is the parsed document-metadata update of the columns
// Artist and Label share. Each domain parses the individual fields in its
// own order, so the first invalid field it reports stays the same.
type MetadataColumns struct {
	Slug           NullableStringUpdate
	CountryCode    NullableStringUpdate
	Website        NullableStringUpdate
	SocialLinks    map[string]string
	SetSocialLinks bool
}

// Complete validates a set slug and takes the social links when the update
// carries them (present).
func (s Spec) Complete(columns *MetadataColumns, socialLinksPresent bool, socialLinks map[string]string) error {
	if columns.Slug.Value != nil {
		if err := pathslug.Validate(*columns.Slug.Value); err != nil {
			return err
		}
	}
	if socialLinksPresent {
		columns.SocialLinks = maps.Clone(socialLinks)
		if columns.SocialLinks == nil {
			columns.SocialLinks = map[string]string{}
		}
		columns.SetSocialLinks = true
	}
	return nil
}

// Present reports whether the update names any shared column.
func (c MetadataColumns) Present() bool {
	return c.Slug.Present || c.CountryCode.Present || c.Website.Present || c.SetSocialLinks
}

// MetadataRow is the current value of the shared metadata columns, read
// under the save's lock.
type MetadataRow struct {
	Slug        *string
	CountryCode *string
	Website     *string
	SocialLinks map[string]string
}

// MetadataChange is what a document-metadata save changes of the shared
// columns.
type MetadataChange struct {
	Slug        Assignment
	CountryCode Assignment
	Website     Assignment
	Parent      Assignment
	// SocialLinks replaces the social links when SetSocialLinks is set.
	SocialLinks    map[string]string
	SetSocialLinks bool
}

// AssignColumns records every changed shared column in change and has
// slugAvailable check a changed slug. It reports whether a shared column
// changed.
func (s Spec) AssignColumns(
	ctx context.Context,
	columns MetadataColumns,
	current MetadataRow,
	change *MetadataChange,
	slugAvailable func(ctx context.Context, slug string) error,
) (bool, error) {
	slugChanged := columns.Slug.Assign(&change.Slug, current.Slug)
	changed := slugChanged
	changed = columns.CountryCode.Assign(&change.CountryCode, current.CountryCode) || changed
	changed = columns.Website.Assign(&change.Website, current.Website) || changed
	if columns.SetSocialLinks && !maps.Equal(current.SocialLinks, columns.SocialLinks) {
		change.SocialLinks, change.SetSocialLinks = columns.SocialLinks, true
		changed = true
	}
	if slugChanged && columns.Slug.Value != nil {
		if err := slugAvailable(ctx, *columns.Slug.Value); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// MetadataSave is one document-metadata save of the entity: Save advances the
// entity's content document and writes its metadata row. A save changes no
// authority, so a parent moves through SetParent.
type MetadataSave[S Store] struct {
	// Access authorizes the save's contributors.
	Access mutation.Access
	// Save advances the content document under the entity's fence and writes
	// the metadata row.
	Save func(ctx context.Context, store S) (contentblock.AdvanceResult, error)
}

// SaveMetadata runs save through the mutation pipeline.
func (m Mutations[S]) SaveMetadata(ctx context.Context, save MetadataSave[S]) (contentblock.AdvanceResult, error) {
	return mutation.Run(ctx, m.runner, mutation.StoreSpec[contentblock.AdvanceResult, S]{
		Access: save.Access,
		Change: save.Save,
		// Telemetry has no document-metadata record; these saves have never
		// been audited.
		Audit: func(contentblock.AdvanceResult) mutation.Audit { return mutation.Unchanged() },
	})
}

package member

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Tag is an administrator-defined Member tag. MemberCount is set only by
// ListMemberTags.
type Tag struct {
	ID          string
	Name        string
	CreatedAt   time.Time
	MemberCount int32
}

// ListMemberTagsQuery pages through Member tags.
type ListMemberTagsQuery struct {
	// Limit defaults to 50 and is capped at 500.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// TagPage is one page of Member tags.
type TagPage struct {
	Tags       []Tag
	Pagination Pagination
}

// ListMemberTags returns one page of tags with their member counts.
func (s *Service) ListMemberTags(ctx context.Context, query ListMemberTagsQuery) (TagPage, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return TagPage{}, err
	}
	limit, offset := 50, 0
	if query.Limit > 0 {
		limit = int(query.Limit)
	}
	if query.Offset > 0 {
		offset = int(query.Offset)
	}
	if limit > 500 {
		limit = 500
	}
	var (
		tags  []Tag
		total int64
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		tags, total, err = store.TagPage(ctx, query.Filters, query.Sorts, limit, offset)
		return err
	}); err != nil {
		return TagPage{}, err
	}
	return TagPage{Tags: tags, Pagination: Pagination{Total: int32(min(total, math.MaxInt32)), Limit: int32(limit), Offset: int32(offset), HasMore: int64(offset+len(tags)) < total}}, nil
}

// CreateMemberTagCommand creates a Member tag.
type CreateMemberTagCommand struct {
	Name string
}

// CreateMemberTag creates the tag and returns it.
func (s *Service) CreateMemberTag(ctx context.Context, command CreateMemberTagCommand) (Tag, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Tag, Store]{
		Access: freshAdmin(),
		Change: func(ctx context.Context, store Store) (Tag, error) {
			name := strings.TrimSpace(command.Name)
			if name == "" {
				return Tag{}, domainerr.Required("name")
			}
			tag, err := store.InsertTag(ctx, name)
			if errors.Is(err, ErrTagNameTaken) {
				return Tag{}, domainerr.Duplicate("member tag", "name", name).WithCause(err)
			}
			return tag, err
		},
		Audit: func(tag Tag) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, tag.ID, tag.Name)
			})
		},
	})
}

// DeleteMemberTagCommand deletes a Member tag that no Member criteria uses.
type DeleteMemberTagCommand struct {
	ID string
}

// DeleteMemberTag deletes the tag and its Member assignments.
func (s *Service) DeleteMemberTag(ctx context.Context, command DeleteMemberTagCommand) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Tag, Store]{
		Access: freshAdmin(),
		Change: func(ctx context.Context, store Store) (Tag, error) {
			tag, err := store.LockTag(ctx, command.ID)
			if errors.Is(err, ErrTagNotFound) {
				return tag, domainerr.ResourceNotFound("member tag", command.ID)
			}
			if err != nil {
				return tag, err
			}
			referenced, err := store.TagReferencedByCriteria(ctx, tag.ID)
			if err != nil {
				return tag, err
			}
			if referenced {
				return tag, domainerr.Precondition("member tag is referenced by a Member criteria")
			}
			return tag, store.DeleteTag(ctx, command.ID)
		},
		Audit: func(tag Tag) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberTagDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagDeletedAuditRecord(metadata, tag.ID, tag.Name)
			})
		},
	})
	return err
}

// SetMemberTagsCommand replaces the tags assigned to a Member.
type SetMemberTagsCommand struct {
	MemberID string
	TagIDs   []string
}

// SetMemberTags applies command and returns the refreshed administrator view.
func (s *Service) SetMemberTags(ctx context.Context, command SetMemberTagsCommand) (AdminMember, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return AdminMember{}, err
	}
	if _, err := s.replaceMemberTags(ctx, command.MemberID, command.TagIDs); err != nil {
		return AdminMember{}, err
	}
	return s.adminMember(ctx, command.MemberID)
}

func (s *Service) replaceMemberTags(ctx context.Context, memberID string, rawTagIDs []string) ([]string, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return nil, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	tagIDs, err := memberTagIDs(rawTagIDs)
	if err != nil {
		return nil, err
	}
	target, err := s.requireLinkedTarget(ctx, memberID, false)
	if err != nil {
		return nil, err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Subjects: []string{target.IdentityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, store Store) (bool, error) {
			if err := recheckTarget(ctx, store, memberID, target, linkedMember(false)); err != nil {
				return false, err
			}
			return storeMemberTags(ctx, store, memberID, tagIDs)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagsUpdatedAuditRecord(metadata, memberID, tagIDs)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return tagIDs, nil
}

// memberTagIDs validates requested tag IDs and returns them deduplicated
// and sorted, the order the mapping is compared in.
func memberTagIDs(rawTagIDs []string) ([]string, error) {
	tagIDs := make([]string, 0, len(rawTagIDs))
	seen := make(map[string]struct{}, len(rawTagIDs))
	for _, tagID := range rawTagIDs {
		if _, err := uuidutil.ParseCanonical(tagID, "tag_id"); err != nil {
			return nil, domainerr.InvalidField("tag_ids", "must contain canonical UUIDs")
		}
		if _, exists := seen[tagID]; !exists {
			seen[tagID] = struct{}{}
			tagIDs = append(tagIDs, tagID)
		}
	}
	sort.Strings(tagIDs)
	return tagIDs, nil
}

// storeMemberTags replaces the Member's tag mapping with the existing tags
// tagIDs and reports whether it changed.
func storeMemberTags(ctx context.Context, store Store, memberID string, tagIDs []string) (bool, error) {
	if len(tagIDs) != 0 {
		count, err := store.CountTags(ctx, tagIDs)
		if err != nil {
			return false, err
		}
		if count != int64(len(tagIDs)) {
			return false, domainerr.InvalidField("tag_ids", "contains an unknown member tag")
		}
	}
	current, err := store.TagIDs(ctx, []string{memberID})
	if err != nil {
		return false, err
	}
	if slices.Equal(current[memberID], tagIDs) {
		return false, nil
	}
	return true, store.ReplaceMemberTags(ctx, memberID, tagIDs)
}

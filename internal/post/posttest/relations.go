package posttest

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/post"
)

func routeTaken(slug string) error { return errs.SlugAlreadyExists("post", slug) }

func (s *Store) SourceLocaleStates(_ context.Context, postIDs []string) (map[string]post.SourceLocaleState, error) {
	if err := s.fail("SourceLocaleStates"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	states := map[string]post.SourceLocaleState{}
	for _, postID := range trimmed(postIDs) {
		stored, ok := s.posts[postID]
		if !ok {
			continue
		}
		if row, ok := s.locales[localeKey{postID, stored.SourceLocale}]; ok {
			states[postID] = post.SourceLocaleState{Title: row.Title, Summary: row.Summary, OgAssetID: row.OgAssetID}
		}
	}
	return states, nil
}

func (s *Store) UpsertSourceLocale(_ context.Context, postID, locale string, title, summary *string, now time.Time) error {
	return s.upsertLocale(postID, locale, title, summary, now)
}

func (s *Store) upsertLocale(postID, locale string, title, summary *string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{postID, locale}
	row, ok := s.locales[key]
	if !ok {
		row.CreatedAt = now
	}
	row.Locale, row.Title, row.Summary, row.UpdatedAt = locale, title, summary, now
	s.locales[key] = row
	return nil
}

func (s *Store) LocaleMetadata(_ context.Context, postID, locale string, _ bool) (post.LocaleMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{postID, locale}]
	if !ok {
		return post.LocaleMetadata{}, post.ErrLocaleNotFound
	}
	return row.LocaleMetadata, nil
}

func (s *Store) OptionalLocaleMetadata(_ context.Context, postID, locale string, _ bool) (post.LocaleMetadata, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{postID, locale}]
	return row.LocaleMetadata, ok, nil
}

func (s *Store) LocaleText(_ context.Context, postID, locale string, _ bool) (post.AIDocumentLocaleMetadata, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{postID, locale}]
	return post.AIDocumentLocaleMetadata{Title: row.Title, Summary: row.Summary}, ok, nil
}

func (s *Store) CountLocale(_ context.Context, postID, locale string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.locales[localeKey{postID, locale}]; ok {
		return 1, nil
	}
	return 0, nil
}

func (s *Store) UpdateLocaleMetadata(_ context.Context, postID, locale string, change post.LocaleChange) (int64, error) {
	if err := s.fail("UpdateLocaleMetadata"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{postID, locale}
	row, ok := s.locales[key]
	if !ok {
		return 0, nil
	}
	if change.SetTitle {
		row.Title = change.Title
	}
	if change.SetSummary {
		row.Summary = change.Summary
	}
	row.UpdatedAt = change.UpdatedAt
	s.locales[key] = row
	return 1, nil
}

func (s *Store) InsertLocale(_ context.Context, postID, locale string, title, summary *string, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{postID, locale}
	if _, ok := s.locales[key]; ok {
		return 0, errs.AlreadyExistsMsg("post_translation already exists")
	}
	s.locales[key] = localeRow{LocaleMetadata: post.LocaleMetadata{Locale: locale, Title: title, Summary: summary, UpdatedAt: now}, CreatedAt: now}
	return 1, nil
}

func (s *Store) InsertEmptyLocale(ctx context.Context, postID, locale string, now time.Time) error {
	_, err := s.InsertLocale(ctx, postID, locale, nil, nil, now)
	return err
}

func (s *Store) UpsertLocaleText(_ context.Context, postID, locale string, title, summary *string, now time.Time) error {
	return s.upsertLocale(postID, locale, title, summary, now)
}

func (s *Store) DeleteLocale(_ context.Context, postID, locale string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{postID, locale}
	if _, ok := s.locales[key]; !ok {
		return 0, nil
	}
	delete(s.locales, key)
	return 1, nil
}

func (s *Store) TaxonomyIDs(_ context.Context, postID string) ([]string, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	categories := slices.Clone(s.links[post.Categories][postID])
	tags := slices.Clone(s.links[post.Tags][postID])
	slices.Sort(categories)
	slices.Sort(tags)
	if categories == nil {
		categories = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	return categories, tags, nil
}

func (s *Store) CountTaxonomy(_ context.Context, taxonomy post.Taxonomy, ids []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range ids {
		if s.taxonomy[taxonomy][id] {
			seen[id] = true
		}
	}
	return int64(len(seen)), nil
}

func (s *Store) InsertTaxonomyLinks(_ context.Context, taxonomy post.Taxonomy, postID string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links[taxonomy][postID] = append(s.links[taxonomy][postID], ids...)
	return nil
}

func (s *Store) ReplaceTaxonomyLinks(_ context.Context, taxonomy post.Taxonomy, postID string, ids []string) error {
	if err := s.fail("ReplaceTaxonomyLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) == 0 {
		delete(s.links[taxonomy], postID)
		return nil
	}
	s.links[taxonomy][postID] = slices.Clone(ids)
	return nil
}

func (s *Store) AuthorLinks(_ context.Context, postIDs []string) ([]post.AuthorLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type author struct {
		post.AuthorLink
		createdAt time.Time
	}
	var authors []author
	for key, createdAt := range s.relations {
		if key.role == post.RoleAuthor && slices.Contains(postIDs, key.postID) {
			authors = append(authors, author{post.AuthorLink{PostID: key.postID, MemberID: key.memberID}, createdAt})
		}
	}
	sort.Slice(authors, func(i, j int) bool {
		switch {
		case authors[i].PostID != authors[j].PostID:
			return authors[i].PostID < authors[j].PostID
		case !authors[i].createdAt.Equal(authors[j].createdAt):
			return authors[i].createdAt.Before(authors[j].createdAt)
		default:
			return authors[i].MemberID < authors[j].MemberID
		}
	})
	links := make([]post.AuthorLink, len(authors))
	for i, author := range authors {
		links[i] = author.AuthorLink
	}
	return links, nil
}

func (s *Store) CommentCounts(_ context.Context, postIDs []string) (map[string]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, comment := range s.comments {
		if slices.Contains(postIDs, comment.PostID) {
			counts[comment.PostID]++
		}
	}
	return counts, nil
}

func (s *Store) ListParticipants(_ context.Context, postID string) ([]post.ParticipantRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []post.ParticipantRow
	for key, createdAt := range s.relations {
		if key.postID != postID {
			continue
		}
		member := s.members[key.memberID]
		rows = append(rows, post.ParticipantRow{
			MemberID: key.memberID, IdentityID: member.IdentityID, Role: key.role, CreatedAt: createdAt,
			HasEffectiveAuthority: member.Active && member.IdentityID != "",
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		switch {
		case rows[i].Role != rows[j].Role:
			return rows[i].Role == post.RoleAuthor
		case !rows[i].CreatedAt.Equal(rows[j].CreatedAt):
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		default:
			return rows[i].MemberID < rows[j].MemberID
		}
	})
	return rows, nil
}

func (s *Store) ParticipantCreatedAt(_ context.Context, postID, memberID string, role post.ParticipantRole) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.relations[relationKey{postID, memberID, role}], nil
}

func (s *Store) ParticipantExists(_ context.Context, postID, memberID string, role post.ParticipantRole) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.relations[relationKey{postID, memberID, role}]
	return ok, nil
}

func (s *Store) CountAuthors(_ context.Context, postID, memberID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for key := range s.relations {
		if key.postID == postID && key.role == post.RoleAuthor && (memberID == "" || key.memberID == memberID) {
			count++
		}
	}
	return count, nil
}

func (s *Store) InsertCreatorAuthor(_ context.Context, postID, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relations[relationKey{postID, memberID, post.RoleAuthor}] = s.now()
	return nil
}

func (s *Store) InsertParticipant(_ context.Context, postID, memberID string, role post.ParticipantRole, createdAt time.Time) error {
	if err := s.fail("InsertParticipant"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := relationKey{postID, memberID, role}
	if _, ok := s.relations[key]; ok {
		return errs.AlreadyExistsMsg("post participant already exists")
	}
	s.relations[key] = createdAt
	return nil
}

func (s *Store) DeleteParticipant(_ context.Context, postID, memberID string, role post.ParticipantRole) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := relationKey{postID, memberID, role}
	if _, ok := s.relations[key]; !ok {
		return 0, nil
	}
	delete(s.relations, key)
	return 1, nil
}

func (s *Store) LockMemberIdentity(_ context.Context, memberID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[memberID]
	if !ok || !member.Active || member.IdentityID == "" {
		return "", errs.NotFound("member", memberID)
	}
	return member.IdentityID, nil
}

func (s *Store) LockLinkedMemberIdentity(_ context.Context, memberID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[memberID]
	if !ok || member.IdentityID == "" {
		return "", errs.NotFound("member", memberID)
	}
	return member.IdentityID, nil
}

func (s *Store) MemberNicknames(_ context.Context, memberIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := map[string]string{}
	for _, memberID := range memberIDs {
		if member, ok := s.members[memberID]; ok && strings.TrimSpace(member.Nickname) != "" {
			names[memberID] = strings.TrimSpace(member.Nickname)
		}
	}
	return names, nil
}

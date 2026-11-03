package mediaassettest

import (
	"context"
	"slices"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

type DownloadStore struct {
	Sources      []mediaasset.FileDownloadSource
	CriteriaRows map[string]mediaasset.DownloadCriteria
	Links        []mediaasset.DownloadAssociation
	Members      map[string]mediaasset.DownloadMemberFacts
	Tags         map[string]map[string]bool
	Fail         error
}

func NewDownload() *DownloadStore {
	return &DownloadStore{CriteriaRows: map[string]mediaasset.DownloadCriteria{}, Members: map[string]mediaasset.DownloadMemberFacts{}, Tags: map[string]map[string]bool{}}
}
func (s *DownloadStore) ContentSources(_ context.Context, ids []string) ([]mediaasset.FileDownloadSource, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.FileDownloadSource
	for _, row := range s.Sources {
		if slices.Contains(ids, row.BlockID) {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func (s *DownloadStore) TrackSources(_ context.Context, ids []string) ([]mediaasset.FileDownloadSource, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.FileDownloadSource
	for _, row := range s.Sources {
		if slices.Contains(ids, row.TrackID) {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func (s *DownloadStore) Associations(_ context.Context, sources map[string]mediaasset.FileDownloadSource, activeOnly bool) ([]mediaasset.DownloadAssociation, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.DownloadAssociation
	for _, link := range s.Links {
		if _, exists := sources[link.PolicyKey]; !exists {
			continue
		}
		if activeOnly {
			criteria, exists := s.CriteriaRows[link.MemberCriteriaID]
			if !exists || criteria.ArchivedAt != nil {
				continue
			}
		}
		rows = append(rows, link)
	}
	return rows, nil
}
func (s *DownloadStore) CriteriaSet(_ context.Context, ids []string, activeOnly bool) ([]mediaasset.DownloadCriteria, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.DownloadCriteria
	for _, id := range ids {
		if row, exists := s.CriteriaRows[id]; exists && (!activeOnly || row.ArchivedAt == nil) {
			row.Config.MemberTagIDs = slices.Clone(row.Config.MemberTagIDs)
			row.Config.AccountRoles = slices.Clone(row.Config.AccountRoles)
			row.Config.ExcludeMemberIDs = slices.Clone(row.Config.ExcludeMemberIDs)
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func (s *DownloadStore) MemberFacts(_ context.Context, memberID, identityID string) (mediaasset.DownloadMemberFacts, bool, error) {
	if s.Fail != nil {
		return mediaasset.DownloadMemberFacts{}, false, s.Fail
	}
	row, found := s.Members[memberID]
	return row, found && row.Onboarded && row.IdentityID == identityID, nil
}
func (s *DownloadStore) TagFacts(_ context.Context, memberID string, ids []string) ([]mediaasset.DownloadTagFact, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.DownloadTagFact
	for _, id := range ids {
		if members, exists := s.Tags[id]; exists {
			row := mediaasset.DownloadTagFact{TagID: id}
			if members[memberID] {
				row.MappedMemberID = new(memberID)
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func (s *DownloadStore) ViewerTagIDs(_ context.Context, memberID string) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var ids []string
	for id, members := range s.Tags {
		if members[memberID] {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (s *DownloadStore) LockCriteriaSet(context.Context, []string) error { return s.Fail }
func (s *DownloadStore) LockViewer(ctx context.Context, user *auth.UserInfo, _ bool) (bool, error) {
	_, found, err := s.MemberFacts(ctx, user.MemberID.String(), user.IdentityID.String())
	return found, err
}

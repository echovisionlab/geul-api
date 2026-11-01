package filemediatest

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

type PolicyRecord struct {
	Relation   domain.PolicyRelation
	SegmentIDs []string
}
type PolicyCheck struct {
	Kind, ID string
	Edit     bool
}

func PolicyKey(s domain.PolicySelector) string {
	value := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return s.OwnerKind + "\x00" + s.EntityID + "\x00" + value(s.BlockID) + "\x00" + value(s.ReferencePath)
}
func (s *LibraryStore) ResolveDownloadPolicyRelation(_ context.Context, selector domain.PolicySelector, _ bool) (domain.PolicyRelation, error) {
	if s.Fail != nil {
		return domain.PolicyRelation{}, s.Fail
	}
	row, ok := s.Policies[PolicyKey(selector)]
	if !ok {
		return domain.PolicyRelation{}, domain.ErrPolicyRelationNotFound
	}
	file, exists := s.Files[row.Relation.FileID]
	if !exists || file.PendingDeletion {
		return domain.PolicyRelation{}, domain.ErrPolicyRelationNotFound
	}
	row.Relation.Selector = selector
	return row.Relation, nil
}
func (s *LibraryStore) DownloadPolicySegmentIDs(_ context.Context, selector domain.PolicySelector) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	return slices.Sorted(slices.Values(s.Policies[PolicyKey(selector)].SegmentIDs)), nil
}
func (s *LibraryStore) SetDownloadPolicy(_ context.Context, selector domain.PolicySelector, audience mediaasset.FileDownloadAudience, ids []string, _ time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	key := PolicyKey(selector)
	if r, ok := s.Policies[key]; ok {
		r.Relation.Audience = audience
		r.SegmentIDs = slices.Clone(ids)
		s.Policies[key] = r
		s.Writes++
	}
	return nil
}
func (s *LibraryStore) ActivePolicySegments(_ context.Context, ids []string) ([]domain.PolicySegment, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	rows := []domain.PolicySegment{}
	for _, id := range ids {
		if row, ok := s.Segments[id]; ok && row.ArchivedAt == nil {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b domain.PolicySegment) int {
		if n := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return rows, nil
}
func (s *LibraryStore) ValidatePolicySegments(_ context.Context, ids []string) error {
	s.ValidatedPolicySegments = append(s.ValidatedPolicySegments, slices.Clone(ids))
	if s.Fail != nil {
		return s.Fail
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	rows := []domain.PolicySegment{}
	for _, id := range ids {
		if row, ok := s.Segments[id]; ok {
			rows = append(rows, row)
		}
	}
	if len(ids) != len(rows) {
		return domainerr.InvalidField("audience_segment_ids", "one or more audience segments do not exist or cannot grant authenticated access")
	}
	for _, row := range rows {
		if !row.Eligible || row.ArchivedAt != nil {
			return domainerr.InvalidField("audience_segment_ids", "one or more audience segments do not exist or cannot grant authenticated access")
		}
	}
	return nil
}
func (s *LibraryStore) RequirePolicyOwner(_ context.Context, kind, id string, edit bool) error {
	s.PolicyChecks = append(s.PolicyChecks, PolicyCheck{kind, id, edit})
	if s.Fail != nil {
		return s.Fail
	}
	return s.PolicyDenied[kind+"\x00"+id]
}
func (s *LibraryStore) PublishPolicyChange(_ context.Context, signal domain.PolicyChangeSignal) error {
	if s.Fail != nil {
		return s.Fail
	}
	if s.PolicySignalError != nil {
		return s.PolicySignalError
	}
	s.PolicySignals = append(s.PolicySignals, signal)
	return nil
}
func clonePolicies(rows map[string]PolicyRecord) map[string]PolicyRecord {
	out := maps.Clone(rows)
	for key, row := range out {
		row.SegmentIDs = slices.Clone(row.SegmentIDs)
		out[key] = row
	}
	return out
}

package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

func (s *Service) ingestService(session domain.IngestSession) (*Service, error) {
	scoped := *s
	scoped.units = session.Units
	scoped.uploadReads = session.Units
	scoped.faviconCommits = session.FaviconCommits
	if s.storeMutations != nil {
		runner, err := s.storeMutations.WithUnits(session.Units)
		if err != nil {
			return nil, err
		}
		scoped.storeMutations = runner
	}
	return &scoped, nil
}

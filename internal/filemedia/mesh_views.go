package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type MeshCandidateView struct {
	Record   domain.MeshCandidate
	Delivery *Delivery
}

type MeshCandidateGeneration struct {
	Candidate          *MeshCandidateView
	CacheHit, Enqueued bool
}

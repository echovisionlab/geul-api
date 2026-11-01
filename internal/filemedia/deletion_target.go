package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type ObjectTarget = domain.ObjectTarget
type GenerationTarget = domain.GenerationTarget
type DeletionTarget = domain.DeletionTarget

func CanonicalObjectTarget(file File) (*ObjectTarget, error) {
	return domain.CanonicalObjectTarget(file)
}

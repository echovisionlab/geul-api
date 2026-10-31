package public

import (
	stderrors "errors"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// Deps are the collaborators of the public catalog reads; all are required.
type Deps struct {
	DB     *gorm.DB
	Assets AssetReader
}

// Service holds the public Category, Tag, Client and Map Place reads.
type Service struct {
	db     *gorm.DB
	assets AssetReader
}

// NewService validates deps and returns the public catalog reads.
func NewService(deps Deps) (*Service, error) {
	var missing []string
	for name, absent := range map[string]bool{"DB": deps.DB == nil, "Assets": deps.Assets == nil} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, stderrors.New("public reference catalog dependencies are required: " + strings.Join(missing, ", "))
	}
	return &Service{db: deps.DB, assets: deps.Assets}, nil
}

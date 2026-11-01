package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type Store = domain.Store
type FileFolder = domain.FileFolder

var (
	ErrFolderNotFound   = domain.ErrFolderNotFound
	ErrFolderNameExists = domain.ErrFolderNameExists
	ErrFolderCycle      = domain.ErrFolderCycle
)

type Joined = domain.Joined

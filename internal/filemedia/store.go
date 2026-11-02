package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type Store = domain.Store
type FileFolder = domain.FileFolder
type FolderView = domain.FolderView

var (
	ErrFolderNotFound   = domain.ErrFolderNotFound
	ErrFolderNameExists = domain.ErrFolderNameExists
	ErrFolderCycle      = domain.ErrFolderCycle
)

type Joined = domain.Joined

type UsageDomain = domain.UsageDomain
type UsageView = domain.UsageView
type UsageDomainCount = domain.UsageDomainCount
type DeletionImpact = domain.DeletionImpact

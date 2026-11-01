package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

func ExportLibraryService(units mutation.UnitOfWork[Store], authorizer mutation.Authorizer, fence identitystate.Fence, audit domainaudit.Appender, members MemberSummaries) *Service {
	runner, err := mutation.NewStoreRunner(units, authorizer, fence, audit)
	if err != nil {
		panic(err)
	}
	return &Service{units: units, storeMutations: runner, memberSummaries: members}
}

var ExportRenameLockedFile = renameLockedFile
var ExportMoveLockedFiles = moveLockedFiles

package filemediatest

import (
	"slices"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeUploadStoreContract(t *testing.T) {
	RunUploadContract(t, func(*testing.T) UploadHarness {
		s := NewLibraryStore()
		return UploadHarness{Harness: mutationtest.FakeHarness[domain.Store](s), Seed: func(_ *testing.T, row domain.UploadSession, parts []domain.UploadPart) {
			s.UploadSessions[row.UploadID] = cloneUpload(row)
			s.UploadPartRows[row.UploadID] = slices.Clone(parts)
		}}
	})
}

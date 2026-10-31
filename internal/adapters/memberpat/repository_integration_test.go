//go:build integration

package memberpat

import (
	"crypto/rand"
	"testing"

	"github.com/google/uuid"

	patdomain "github.com/echovisionlab/geul-api/internal/member/pat"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

// PostgreSQL keeps microseconds. Regenerate compares the stored updated_at with
// the clock value it wrote, so the system clock must produce stored precision.
func TestServiceRegeneratesWithSystemClockOnPostgreSQLIntegration(t *testing.T) {
	t.Parallel()

	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	memberID := uuid.NewString()
	if err := postgres.DB.Exec("INSERT INTO member (id, nickname) VALUES (?::uuid, ?)", memberID, "PAT owner").Error; err != nil {
		t.Fatalf("insert member: %v", err)
	}
	repository := mustPATRepository(t, postgres.DB)
	service, err := patdomain.NewService(repository, SystemClock{}, rand.Reader)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	created, err := service.Create(t.Context(), patdomain.MemberID(memberID))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	listed, err := service.List(t.Context(), patdomain.MemberID(memberID))
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	if !listed[0].CreatedAt.Equal(created.Metadata.CreatedAt) || !listed[0].UpdatedAt.Equal(created.Metadata.UpdatedAt) {
		t.Fatalf("List() metadata = %+v, want Create() timestamps %+v", listed[0], created.Metadata)
	}

	regenerated, err := service.Regenerate(t.Context(), patdomain.MemberID(memberID), created.Metadata.ID)
	if err != nil {
		t.Fatalf("Regenerate() error = %v", err)
	}
	listed, err = service.List(t.Context(), patdomain.MemberID(memberID))
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() after Regenerate = %+v, %v", listed, err)
	}
	if !listed[0].UpdatedAt.Equal(regenerated.Metadata.UpdatedAt) {
		t.Fatalf("List() UpdatedAt = %v, want Regenerate() UpdatedAt %v", listed[0].UpdatedAt, regenerated.Metadata.UpdatedAt)
	}
}

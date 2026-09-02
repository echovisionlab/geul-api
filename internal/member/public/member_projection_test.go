package public

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestProjectPublicDeletedMemberAlwaysScrubsPIIAndAvatar(t *testing.T) {
	nickname := "FormerNickname"
	deletedAt := time.Now().UTC()
	summary, err := projectPublicMemberSummary(model.Member{
		ID:        uuid.NewString(),
		Nickname:  nickname,
		DeletedAt: &deletedAt,
	}, &commonv1.AssetRef{AssetId: uuid.NewString()})
	if err != nil {
		t.Fatalf("project deleted Member: %v", err)
	}
	if !summary.Deleted {
		t.Fatal("deleted marker was not projected")
	}
	if summary.GetNickname() != nickname {
		t.Fatalf("nickname = %q, want %q", summary.GetNickname(), nickname)
	}
	if summary.AvatarAsset != nil {
		t.Fatal("deleted Member avatar was not scrubbed")
	}
}

func TestProjectPublicUnlinkedMemberIsImmediatelyTombstoned(t *testing.T) {
	nickname := "UnlinkedNickname"
	summary, err := projectPublicMemberSummary(model.Member{
		ID:       uuid.NewString(),
		Nickname: nickname,
	}, &commonv1.AssetRef{AssetId: uuid.NewString()})
	if err != nil {
		t.Fatalf("project unlinked Member: %v", err)
	}
	if !summary.Deleted {
		t.Fatal("unlinked Member was not projected as deleted")
	}
	if summary.GetNickname() != nickname {
		t.Fatalf("nickname = %q, want %q", summary.GetNickname(), nickname)
	}
	if summary.AvatarAsset != nil {
		t.Fatal("unlinked Member avatar was not scrubbed")
	}
}

func TestProjectUnonboardedMemberShapeKeepsUUIDNickname(t *testing.T) {
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	avatar := &commonv1.AssetRef{AssetId: uuid.NewString()}
	summary, err := projectPublicMemberSummary(model.Member{
		ID:                memberID,
		AccountIdentityID: &identityID,
		Nickname:          memberID,
	}, avatar)
	if err != nil {
		t.Fatalf("project active Member awaiting onboarding: %v", err)
	}
	if summary.Deleted {
		t.Fatal("active linked Member was projected as deleted")
	}
	if summary.GetNickname() != memberID {
		t.Fatalf("nickname = %q, want UUID default", summary.GetNickname())
	}
	if summary.AvatarAsset != avatar {
		t.Fatal("active Member avatar was unexpectedly scrubbed")
	}
}

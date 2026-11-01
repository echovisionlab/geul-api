package public

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/member"
)

func TestProjectPublicDeletedMemberAlwaysScrubsPIIAndAvatar(t *testing.T) {
	nickname := "FormerNickname"
	deletedAt := time.Now().UTC()
	summary := projectPublicMemberSummary(member.Member{
		ID:        uuid.NewString(),
		Nickname:  nickname,
		DeletedAt: &deletedAt,
	}, &member.Asset{ID: uuid.NewString()})
	if !summary.Deleted {
		t.Fatal("deleted marker was not projected")
	}
	if summary.Nickname != nickname {
		t.Fatalf("nickname = %q, want %q", summary.Nickname, nickname)
	}
	if summary.Avatar != nil {
		t.Fatal("deleted Member avatar was not scrubbed")
	}
}

func TestProjectPublicUnlinkedMemberIsImmediatelyTombstoned(t *testing.T) {
	nickname := "UnlinkedNickname"
	summary := projectPublicMemberSummary(member.Member{
		ID:       uuid.NewString(),
		Nickname: nickname,
	}, &member.Asset{ID: uuid.NewString()})
	if !summary.Deleted {
		t.Fatal("unlinked Member was not projected as deleted")
	}
	if summary.Nickname != nickname {
		t.Fatalf("nickname = %q, want %q", summary.Nickname, nickname)
	}
	if summary.Avatar != nil {
		t.Fatal("unlinked Member avatar was not scrubbed")
	}
}

func TestProjectUnonboardedMemberShapeKeepsUUIDNickname(t *testing.T) {
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	avatar := &member.Asset{ID: uuid.NewString()}
	summary := projectPublicMemberSummary(member.Member{
		ID:                memberID,
		AccountIdentityID: &identityID,
		Nickname:          memberID,
	}, avatar)
	if summary.Deleted {
		t.Fatal("active linked Member was projected as deleted")
	}
	if summary.Nickname != memberID {
		t.Fatalf("nickname = %q, want UUID default", summary.Nickname)
	}
	if summary.Avatar != avatar {
		t.Fatal("active Member avatar was unexpectedly scrubbed")
	}
}

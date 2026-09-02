package emailauthoring

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

func emailTestActorID(ctx context.Context) string {
	if user := auth.GetUser(ctx); user != nil && strings.TrimSpace(user.MemberID.String()) != "" {
		return user.MemberID.String()
	}
	return "system"
}

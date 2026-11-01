package sharelink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// VerifyLink verifies a bounded link and its optional Argon2id password
// proof for one target. Invalid, expired, deleted, mismatched and wrong
// password proofs all answer ErrLinkNotFound, so callers cannot tell them
// apart.
func VerifyLink(ctx context.Context, links Store, token, password string, entityType EntityType, entityID string) (Link, error) {
	link, err := links.LiveLink(ctx, token, entityType, entityID, time.Now())
	if err != nil {
		if errors.Is(err, ErrLinkNotFound) {
			return Link{}, err
		}
		// Some callers answer every failure as not found; the mark keeps a
		// server fault at Error in the failure record.
		return Link{}, validationFault(fmt.Errorf("load share link: %w", err), entityType, entityID, token, password)
	}
	if link.PasswordHash == nil {
		return link, nil
	}
	if password == "" {
		return Link{}, ErrLinkNotFound
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
	if err != nil {
		return Link{}, validationFault(fmt.Errorf("verify share link password: %w", err), entityType, entityID, token, password, *link.PasswordHash)
	}
	if !match {
		return Link{}, ErrLinkNotFound
	}
	return link, nil
}

func validationFault(err error, entityType EntityType, entityID string, private ...string) error {
	return failurecause.ServerFault(failurecause.WithAttrs(failurecause.WithPrivate(err, private...),
		slog.String("entity_type", string(entityType)), slog.String("entity_id", entityID)))
}

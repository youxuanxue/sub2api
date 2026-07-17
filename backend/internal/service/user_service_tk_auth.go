package service

import (
	"context"
	"fmt"
)

// userAuthRepo is an optional capability the concrete user repository implements
// (repository.userRepository.GetByIDForAuth) to support a lean authentication
// lookup. It is intentionally NOT added to the shared UserRepository interface so
// the many existing test stubs that implement UserRepository do not all need a
// new method; a repo that does not implement it transparently falls back to the
// regular GetByID below (correctness over speed).
type userAuthRepo interface {
	GetByIDForAuth(ctx context.Context, id int64) (*User, error)
}

// GetByIDForAuth resolves the caller for the authentication hot path with the
// minimum work the auth decision needs: a single users-row lookup. It skips the
// allowed-groups join (repo layer) and the avatar SELECT (service layer) that the
// general-purpose GetByID performs, because the JWT / admin middleware only reads
// scalar fields (ID, Status, Role, Concurrency, TokenVersion, LastActiveAt). This
// cuts the per-request auth DB cost from 3 round trips to 1.
//
// Security note: this deliberately does NOT cache the user row — TokenVersion and
// Status must reflect password changes / deactivation immediately. The entire win
// comes from dropping the two enrichment queries, never from caching identity.
func (s *UserService) GetByIDForAuth(ctx context.Context, id int64) (*User, error) {
	var (
		user *User
		err  error
	)
	if ar, ok := s.userRepo.(userAuthRepo); ok {
		user, err = ar.GetByIDForAuth(ctx, id)
	} else {
		user, err = s.userRepo.GetByID(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	normalizeLoadedUserTokenVersion(user)
	return user, nil
}

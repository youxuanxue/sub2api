package repository

import (
	"context"

	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetByIDForAuth loads a user by ID for the authentication hot path WITHOUT the
// enrichment round trips GetByID performs. GetByID issues three DB round trips
// per call — the users row, a UserAllowedGroup join (loadAllowedGroups), and a
// user_avatars SELECT (hydrateUserAvatar, in the service layer). The JWT / admin
// auth middleware only reads scalar user columns (ID, Status, Role, Concurrency,
// TokenVersion, LastActiveAt), every one of which userEntityToService already
// maps from the single users row. Skipping the allowed-groups load here (and the
// avatar SELECT in the sibling service method) turns the per-request auth cost
// from 3 round trips into 1 — a tax every admin SPA XHR pays, multiplied by the
// page's fan-out.
//
// This is exposed as an optional capability (see service.userAuthRepo in
// user_service_tk_auth.go) rather than added to the shared UserRepository
// interface, so the many existing repo test stubs need no change; the service
// falls back to GetByID when a repo does not implement it.
func (r *userRepository) GetByIDForAuth(ctx context.Context, id int64) (*service.User, error) {
	m, err := r.client.User.Query().Where(dbuser.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, domain.ErrUserNotFound, nil)
	}
	return userEntityToService(m), nil
}

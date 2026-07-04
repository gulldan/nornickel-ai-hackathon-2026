// Admin use cases: the corpus-wide and account-wide listings behind the
// operator/administrator panels.

package application

import (
	"context"

	"github.com/example/main-service/internal/domain"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// AdminService exposes the privileged read paths.
type AdminService struct {
	users domain.UserCatalog
}

// NewAdminService wires the admin dependencies.
func NewAdminService(users domain.UserCatalog) *AdminService {
	return &AdminService{users: users}
}

// ListUsers returns every account (password hashes are never populated).
func (s *AdminService) ListUsers(ctx context.Context) ([]*commonv1.User, error) {
	return s.users.ListUsers(ctx)
}

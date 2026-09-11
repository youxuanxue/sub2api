package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUS054_MachinePolicyUsesRegisteredRouteTemplates(t *testing.T) {
	registered := map[string]bool{}
	for _, route := range newAdminRoutesTestRouter().Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	// Every policy path is an actual method/template pair. The full matrix and
	// negative HTTP authorization behavior are tested in service/middleware.
	for _, permission := range service.MachineAdminPermissions() {
		for _, route := range permission.Routes {
			require.True(t, registered[route], "permission %s references unregistered route %s", permission.Scope, route)
		}
	}
}

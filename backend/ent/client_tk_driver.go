// Package ent
//
// Manual companion file (NOT generated): expose the unexported `driver` field
// via a Driver() accessor. Upstream sub2api uses client.Driver().Exec / Query
// for raw SQL in service-layer code (auth_oauth_first_bind, auth_pending_identity,
// etc.), but neither TK nor upstream's --feature flag set generates this method.
//
// Living in a separate file means `go generate ./ent` does not overwrite it.
package ent

import "entgo.io/ent/dialect"

// Driver returns the dialect.Driver currently configured on the client. It is
// safe to call on both the root client and a tx-scoped client.
func (c *Client) Driver() dialect.Driver {
	return c.driver
}

package setup

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

const tkPostgresMaintenanceDatabase = "postgres"

func tkJoinHostPortForPostgres(host string, port int) string {
	h := strings.TrimSpace(host)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	return net.JoinHostPort(h, strconv.Itoa(port))
}

// tkPostgresConnURL builds a lib/pq-compatible URL so passwords with spaces, '=', '@', etc.
// cannot break parsing (keyword/value DSNs are fragile).
func tkPostgresConnURL(host string, port int, user, password, dbname, sslmode string) string {
	v := url.Values{}
	v.Set("sslmode", sslmode)
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     tkJoinHostPortForPostgres(host, port),
		Path:     "/" + dbname,
		RawQuery: v.Encode(),
	}
	return u.String()
}

package proxy

import (
	"net"

	"rds-iam-proxy/internal/config"

	"github.com/go-mysql-org/go-mysql/server"
)

func authenticateClient(conn net.Conn, p config.Profile) (*server.Conn, error) {
	// NewConn performs MySQL server greeting + auth validation.
	return server.NewConn(conn, p.ProxyUser, p.ProxyPassword, server.EmptyHandler{})
}

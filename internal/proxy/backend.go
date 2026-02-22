package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"rds-iam-proxy/internal/config"
	"rds-iam-proxy/internal/token"

	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
)

type BackendFactory struct {
	profile    config.Profile
	tokenCache *token.Cache
	tlsConfig  *tls.Config
	timeout    time.Duration
}

func NewBackendFactory(p config.Profile, tokenCache *token.Cache, timeout time.Duration) (*BackendFactory, error) {
	tlsCfg, err := buildTLSConfig(p)
	if err != nil {
		return nil, err
	}
	return &BackendFactory{
		profile:    p,
		tokenCache: tokenCache,
		tlsConfig:  tlsCfg,
		timeout:    timeout,
	}, nil
}

func (f *BackendFactory) NewConn(ctx context.Context) (*client.Conn, error) {
	ct, err := f.tokenCache.Get(ctx, f.profile)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(f.profile.RDSHost, strconv.Itoa(f.profile.RDSPort))
	conn, err := client.ConnectWithContext(ctx, addr, f.profile.RDSDBUser, ct.Value, f.profile.DefaultDB, f.timeout, func(c *client.Conn) error {
		// Keep backend command-phase packets compatible with raw forwarding from GUI clients.
		c.UnsetCapability(mysql.CLIENT_QUERY_ATTRIBUTES)
		c.UnsetCapability(mysql.CLIENT_COMPRESS)
		c.UnsetCapability(mysql.CLIENT_ZSTD_COMPRESSION_ALGORITHM)
		c.SetTLSConfig(f.tlsConfig)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("connect backend: %w", err)
	}

	return conn, nil
}

func buildTLSConfig(p config.Profile) (*tls.Config, error) {
	ca, err := os.ReadFile(p.CABundle)
	if err != nil {
		return nil, fmt.Errorf("read ca bundle: %w", err)
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(ca); !ok {
		return nil, fmt.Errorf("invalid PEM in ca bundle %s", p.CABundle)
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: p.RDSHost,
	}, nil
}

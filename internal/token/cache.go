package token

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"rds-iam-proxy/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

var (
	loadDefaultAWSConfig = awsconfig.LoadDefaultConfig
	buildRDSAuthToken    = auth.BuildAuthToken
)

type CachedToken struct {
	Value     string
	ExpiresAt time.Time
}

type Cache struct {
	mu            sync.Mutex
	entries       map[string]CachedToken
	awsProviders  map[string]aws.CredentialsProvider
	refreshBefore time.Duration
	tokenTTL      time.Duration
}

func New(refreshBefore, tokenTTL time.Duration) *Cache {
	return &Cache{
		entries:       map[string]CachedToken{},
		awsProviders:  map[string]aws.CredentialsProvider{},
		refreshBefore: refreshBefore,
		tokenTTL:      tokenTTL,
	}
}

func (c *Cache) Get(ctx context.Context, p config.Profile) (CachedToken, error) {
	key := cacheKey(p)

	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && time.Until(entry.ExpiresAt) > c.refreshBefore {
		c.mu.Unlock()
		return entry, nil
	}
	c.mu.Unlock()

	provider, err := c.getOrInitProvider(ctx, p)
	if err != nil {
		return CachedToken{}, err
	}

	fresh, err := build(ctx, p, c.tokenTTL, provider)
	if err != nil {
		return CachedToken{}, err
	}

	c.mu.Lock()
	c.entries[key] = fresh
	c.mu.Unlock()

	return fresh, nil
}

func (c *Cache) getOrInitProvider(ctx context.Context, p config.Profile) (aws.CredentialsProvider, error) {
	key := providerKey(p)

	c.mu.Lock()
	if provider, ok := c.awsProviders[key]; ok {
		c.mu.Unlock()
		return provider, nil
	}
	c.mu.Unlock()

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(p.RDSRegion),
	}
	if p.AWSProfile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(p.AWSProfile))
	}

	awsCfg, err := loadDefaultAWSConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	c.mu.Lock()
	c.awsProviders[key] = awsCfg.Credentials
	c.mu.Unlock()

	return awsCfg.Credentials, nil
}

func build(ctx context.Context, p config.Profile, ttl time.Duration, provider aws.CredentialsProvider) (CachedToken, error) {
	endpoint := net.JoinHostPort(p.RDSHost, strconv.Itoa(p.RDSPort))
	token, err := buildRDSAuthToken(ctx, endpoint, p.RDSRegion, p.RDSDBUser, provider)
	if err != nil {
		return CachedToken{}, fmt.Errorf("build auth token: %w", err)
	}

	return CachedToken{
		Value:     token,
		ExpiresAt: time.Now().Add(ttl),
	}, nil
}

func cacheKey(p config.Profile) string {
	return p.Name + "|" + p.RDSHost + "|" + strconv.Itoa(p.RDSPort) + "|" + p.RDSRegion + "|" + p.RDSDBUser + "|" + p.AWSProfile
}

func providerKey(p config.Profile) string {
	return p.RDSRegion + "|" + p.AWSProfile
}

package token

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"rds-iam-proxy/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

type staticProvider struct{}

func (staticProvider) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     "AKIA_TEST",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		CanExpire:       false,
	}, nil
}

func TestCacheGetReturnsCachedTokenBeforeRefreshWindow(t *testing.T) {
	origLoad := loadDefaultAWSConfig
	origBuild := buildRDSAuthToken
	t.Cleanup(func() {
		loadDefaultAWSConfig = origLoad
		buildRDSAuthToken = origBuild
	})

	var buildCalls int32
	loadDefaultAWSConfig = func(_ context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{Credentials: staticProvider{}}, nil
	}
	buildRDSAuthToken = func(_ context.Context, _, _, _ string, _ aws.CredentialsProvider, _ ...func(options *auth.BuildAuthTokenOptions)) (string, error) {
		call := atomic.AddInt32(&buildCalls, 1)
		return "token-" + time.Now().Format(time.RFC3339Nano) + "-" + string(rune('0'+call)), nil
	}

	c := New(5*time.Minute, 15*time.Minute)
	p := config.Profile{
		Name:       "p1",
		RDSHost:    "db.example",
		RDSPort:    3306,
		RDSRegion:  "eu-west-1",
		RDSDBUser:  "db_user_1",
		AWSProfile: "dev",
	}

	first, err := c.Get(context.Background(), p)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := c.Get(context.Background(), p)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}

	if first.Value != second.Value {
		t.Fatalf("expected cached token reuse, got %q and %q", first.Value, second.Value)
	}
	if atomic.LoadInt32(&buildCalls) != 1 {
		t.Fatalf("expected single build call, got %d", buildCalls)
	}
}

func TestCacheRefreshesWithinRefreshWindow(t *testing.T) {
	origLoad := loadDefaultAWSConfig
	origBuild := buildRDSAuthToken
	t.Cleanup(func() {
		loadDefaultAWSConfig = origLoad
		buildRDSAuthToken = origBuild
	})

	var buildCalls int32
	loadDefaultAWSConfig = func(_ context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{Credentials: staticProvider{}}, nil
	}
	buildRDSAuthToken = func(_ context.Context, _, _, _ string, _ aws.CredentialsProvider, _ ...func(options *auth.BuildAuthTokenOptions)) (string, error) {
		call := atomic.AddInt32(&buildCalls, 1)
		return "token-call-" + string(rune('0'+call)), nil
	}

	// refreshBefore > tokenTTL forces refresh on each Get.
	c := New(20*time.Minute, 15*time.Minute)
	p := config.Profile{
		Name:      "p1",
		RDSHost:   "db.example",
		RDSPort:   3306,
		RDSRegion: "eu-west-1",
		RDSDBUser: "db_user_1",
	}

	first, err := c.Get(context.Background(), p)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := c.Get(context.Background(), p)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if first.Value == second.Value {
		t.Fatalf("expected refreshed token values to differ, got %q", first.Value)
	}
	if atomic.LoadInt32(&buildCalls) != 2 {
		t.Fatalf("expected two build calls, got %d", buildCalls)
	}
}

func TestProviderCacheIsReusedForSameRegionAndAWSProfile(t *testing.T) {
	origLoad := loadDefaultAWSConfig
	origBuild := buildRDSAuthToken
	t.Cleanup(func() {
		loadDefaultAWSConfig = origLoad
		buildRDSAuthToken = origBuild
	})

	var loadCalls int32
	loadDefaultAWSConfig = func(_ context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		atomic.AddInt32(&loadCalls, 1)
		return aws.Config{Credentials: staticProvider{}}, nil
	}
	buildRDSAuthToken = func(_ context.Context, _, _, _ string, _ aws.CredentialsProvider, _ ...func(options *auth.BuildAuthTokenOptions)) (string, error) {
		return "token", nil
	}

	c := New(20*time.Minute, 15*time.Minute) // force token refresh every call
	p := config.Profile{
		Name:       "p1",
		RDSHost:    "db.example",
		RDSPort:    3306,
		RDSRegion:  "eu-west-1",
		RDSDBUser:  "db_user_1",
		AWSProfile: "team",
	}

	if _, err := c.Get(context.Background(), p); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := c.Get(context.Background(), p); err != nil {
		t.Fatalf("second Get: %v", err)
	}

	if atomic.LoadInt32(&loadCalls) != 1 {
		t.Fatalf("expected single aws config load due to provider cache, got %d", loadCalls)
	}
}

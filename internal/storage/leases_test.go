package storage

import (
	"testing"
	"time"
)

func TestClaimAndFailDefaults(t *testing.T) {
	options, err := (ClaimOptions{Owner: "worker"}).normalized()
	if err != nil || options.BatchSize != 1 || options.LeaseDuration != time.Minute || options.MaxClaims != 3 || options.MaxAttempts != 3 {
		t.Fatalf("claim defaults: %+v %v", options, err)
	}
	for _, o := range []ClaimOptions{{}, {Owner: "worker", BatchSize: -1}, {Owner: "worker", BatchSize: 1001}, {Owner: "worker", LeaseDuration: -1}, {Owner: "worker", MaxClaims: -1}, {Owner: "worker", MaxAttempts: 1001}} {
		if _, err := o.normalized(); err == nil {
			t.Error("invalid claim options accepted")
		}
	}
	policy, err := (FailPolicy{}).normalized()
	if err != nil || policy.InitialBackoff != time.Second || policy.MaxBackoff != 10*time.Second {
		t.Fatalf("fail defaults: %+v %v", policy, err)
	}
	for _, p := range []FailPolicy{{InitialBackoff: -1}, {InitialBackoff: time.Hour, MaxBackoff: time.Second}, {MaxBackoff: 25 * time.Hour}} {
		if _, err := p.normalized(); err == nil {
			t.Error("invalid fail policy accepted")
		}
	}
}

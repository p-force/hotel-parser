package bnovo

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterMetadata(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"5", 5 * time.Second}, {"0", 0}, {"-1", 0}, {"bad", 0}, {"9999999999999999999", 0},
		{now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second}, {now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		if got := parseRetryAfter(tc.raw, now); got != tc.want {
			t.Errorf("Retry-After %q=%s want=%s", tc.raw, got, tc.want)
		}
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "3"); w.WriteHeader(429) })
	result := client.Collect(context.Background(), testSearchRequest())
	if result.Error == nil || result.Error.RetryAfter != 3*time.Second {
		t.Fatalf("metadata lost between Search and Collect: %+v", result.Error)
	}
}

func TestTemporaryTransportClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{io.ErrUnexpectedEOF, true}, {fmt.Errorf("wrapped: %w", io.EOF), true},
		{&net.DNSError{IsTimeout: true}, true}, {&net.DNSError{IsNotFound: true}, false},
		{x509.UnknownAuthorityError{}, false}, {errors.New("permanent transport failure"), false},
	} {
		if got := temporaryTransportError(tc.err); got != tc.want {
			t.Errorf("%T temporary=%v want=%v", tc.err, got, tc.want)
		}
	}
}

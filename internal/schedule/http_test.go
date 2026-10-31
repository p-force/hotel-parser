package schedule

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTriggerRejectsUnauthorizedAndMalformed(t *testing.T) {
	service := &Service{}
	secret := strings.Repeat("s", 32)
	if _, err := service.Handler(""); err == nil {
		t.Fatal("missing secret accepted")
	}
	handler, err := service.Handler(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, auth, body string
		status                   int
	}{
		{"POST", "/trigger", "", `{"action":"resume"}`, 401},
		{"POST", "/trigger?secret=" + secret, "", `{"action":"resume"}`, 401},
		{"POST", "/trigger", "Bearer wrong", `{"action":"resume"}`, 401},
		{"GET", "/trigger", "Bearer " + secret, "", 405},
		{"POST", "/trigger?x=1", "Bearer " + secret, `{"action":"resume"}`, 400},
		{"POST", "/trigger", "Bearer " + secret, `{"action":"resume","unknown":1}`, 400},
		{"POST", "/trigger", "Bearer " + secret, `{"action":"plan"}`, 400},
		{"POST", "/trigger", "Bearer " + secret, `{"action":"wrong"}`, 400},
		{"POST", "/trigger", "Bearer " + secret, `{"action":"resume"} {}`, 400},
		{"POST", "/trigger", "Bearer " + secret, strings.Repeat("x", 4097), 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("status/body: %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("trigger response cacheable")
		}
	}
}

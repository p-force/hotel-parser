//go:build integration

package schedule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type liveReplay struct{ bodies map[string][]byte }

func requestKey(uid, in, out, adults string) string {
	return strings.Join([]string{uid, in, out, adults}, "/")
}
func loadLiveReplay(t *testing.T) *liveReplay {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("HOTEL_PARSER_E2E_REPLAY"))
	if err != nil {
		t.Fatal("replay evidence missing")
	}
	var evidence liveEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	result := &liveReplay{bodies: map[string][]byte{}}
	for _, r := range evidence.Requests {
		if r.HTTPStatus != 200 {
			continue
		}
		body, err := os.ReadFile(filepath.Join(os.Getenv("HOTEL_PARSER_E2E_PRIVATE_RAW"), r.SHA256+".html"))
		if err != nil {
			t.Fatal("private raw body missing")
		}
		hash := sha256.Sum256(body)
		if hex.EncodeToString(hash[:]) != r.SHA256 {
			t.Fatal("raw body checksum mismatch")
		}
		result.bodies[requestKey(r.UID, r.CheckIn, r.CheckOut, r.Adults)] = body
	}
	if len(result.bodies) != 122 {
		t.Fatal("replay requires all 122 completed source bodies")
	}
	return result
}
func (p *liveReplay) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.URL.Query()
	uid := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body, ok := p.bodies[requestKey(uid, q.Get("dfrom"), q.Get("dto"), q.Get("adults"))]
	if !ok {
		return nil, errors.New("no captured response; network fallback is forbidden")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

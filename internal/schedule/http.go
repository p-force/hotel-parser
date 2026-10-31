package schedule

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/p-force/hotel-parser/internal/storage"
)

// Handler не запускает фоновых worker: Execute заканчивается до HTTP-ответа.
// Секрет передаётся вызывающим кодом из env; не принимается из query string.
func (s *Service) Handler(secret string) (http.Handler, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("HOTEL_PARSER_TRIGGER_SECRET must contain at least 32 bytes")
	}
	want := sha256.Sum256([]byte("Bearer " + secret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "query parameters are not accepted", http.StatusBadRequest)
			return
		}
		mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaErr != nil || mediaType != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		var req Request
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if d.Decode(new(any)) != io.EOF {
			http.Error(w, "unexpected content", http.StatusBadRequest)
			return
		}
		if err := ValidateRequest(req); err != nil {
			http.Error(w, "invalid action/slot", http.StatusBadRequest)
			return
		}
		response, err := s.Execute(r.Context(), req)
		status := http.StatusOK
		code := ""
		if err != nil {
			status = http.StatusServiceUnavailable
			code = "collection_incomplete"
			if errors.Is(err, storage.ErrRunnerBusy) {
				status = http.StatusConflict
				code = "runner_busy"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Response
			Error string `json:"error,omitempty"`
		}{response, code})
	}), nil
}

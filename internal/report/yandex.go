package report

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// YandexDiskUploader никогда не сохраняет OAuth или выданные API signed URL.
// Переданный client копируется; redirects запрещены и для API, и для PUT.
type YandexDiskUploader struct {
	token  string
	client *http.Client
}

func NewYandexDiskUploader(client *http.Client) *YandexDiskUploader {
	c := http.Client{Timeout: 15 * time.Second}
	if client != nil {
		c = *client
	}
	c.Timeout = 15 * time.Second
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &YandexDiskUploader{token: os.Getenv("YANDEX_DISK_TOKEN"), client: &c}
}

type diskError struct {
	code       string
	retryAfter time.Duration
	permanent  bool
}

func (e *diskError) Error() string   { return e.code }
func failure(code string) *diskError { return &diskError{code: code} }
func retryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return min(time.Duration(min(seconds, 86400))*time.Second, 24*time.Hour)
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, min(time.Until(at), 24*time.Hour))
	}
	return 0
}
func responseError(r *http.Response) *diskError {
	return &diskError{code: fmt.Sprintf("disk_http_%d", r.StatusCode), retryAfter: retryAfter(r.Header.Get("Retry-After")), permanent: r.StatusCode >= 400 && r.StatusCode < 500 && r.StatusCode != 408 && r.StatusCode != 429 && r.StatusCode != 409}
}
func (u *YandexDiskUploader) api(ctx context.Context, method, endpoint, path string, output any) (int, error) {
	q := url.Values{"path": {path}}
	if endpoint == "/upload" {
		q.Set("overwrite", "false")
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://cloud-api.yandex.net/v1/disk/resources"+endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return 0, failure("disk_request")
	}
	req.Header.Set("Authorization", "OAuth "+u.token)
	res, err := u.client.Do(req)
	if err != nil {
		return 0, failure("disk_network")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, responseError(res)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(output); err != nil {
			return res.StatusCode, failure("disk_response")
		}
	}
	return res.StatusCode, nil
}
func trustedUpload(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".disk.yandex.net") || strings.HasSuffix(host, ".dst.yandex.net")
}

type fingerprint struct {
	size        int64
	md5, sha256 string
}
type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

// hashCSV не удерживает CSV в памяти или на локальном диске.
func hashCSV(ctx context.Context, write func(context.Context, io.Writer) error) (fingerprint, error) {
	m, s, n := md5.New(), sha256.New(), &countWriter{}
	if err := write(ctx, io.MultiWriter(m, s, n)); err != nil {
		return fingerprint{}, failure("csv_read_error")
	}
	return fingerprint{n.n, hex.EncodeToString(m.Sum(nil)), hex.EncodeToString(s.Sum(nil))}, nil
}
func (u *YandexDiskUploader) confirmed(ctx context.Context, path string, want fingerprint) (bool, error) {
	var meta struct {
		Type   string `json:"type"`
		Size   *int64 `json:"size"`
		MD5    string `json:"md5"`
		SHA256 string `json:"sha256"`
	}
	status, err := u.api(ctx, http.MethodGet, "", path, &meta)
	if status == 404 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if meta.Type != "file" || meta.Size == nil || *meta.Size != want.size {
		return false, &diskError{code: "disk_remote_conflict", permanent: true}
	}
	if (meta.SHA256 != "" && strings.EqualFold(meta.SHA256, want.sha256)) || (meta.SHA256 == "" && meta.MD5 != "" && strings.EqualFold(meta.MD5, want.md5)) {
		return true, nil
	}
	return false, &diskError{code: "disk_checksum_unconfirmed", permanent: true}
}
func (u *YandexDiskUploader) upload(ctx context.Context, path string, write func(context.Context, io.Writer) error) error {
	if strings.IndexFunc(u.token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return &diskError{code: "disk_token_invalid", permanent: true}
	}
	want, err := hashCSV(ctx, write)
	if err != nil {
		return err
	}
	ok, err := u.confirmed(ctx, path, want)
	if err != nil || ok {
		return err
	}
	// Директории могут уже существовать. 409 принимается только после проверки type.
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := 1; i < len(parts); i++ {
		dir := "/" + strings.Join(parts[:i], "/")
		status, e := u.api(ctx, http.MethodPut, "", dir, nil)
		if status == 409 {
			var meta struct {
				Type string `json:"type"`
			}
			_, e = u.api(ctx, http.MethodGet, "", dir, &meta)
			if e == nil && meta.Type != "dir" {
				e = &diskError{code: "disk_directory_conflict", permanent: true}
			}
		}
		if e != nil {
			return e
		}
	}
	var link struct {
		Href      string `json:"href"`
		Method    string `json:"method"`
		Templated bool   `json:"templated"`
	}
	_, err = u.api(ctx, http.MethodGet, "/upload", path, &link)
	if err != nil {
		return err
	}
	if !trustedUpload(link.Href) || link.Method != "PUT" || link.Templated {
		return &diskError{code: "disk_untrusted_href", permanent: true}
	}
	// Каждая попытка заново читает immutable run. Закрываем обе стороны и ждём
	// producer даже при раннем HTTP-отказе; фонового export после возврата нет.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { err := write(streamCtx, writer); writer.CloseWithError(err); done <- err }()
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodPut, link.Href, reader)
	req.ContentLength = want.size
	req.Header.Set("Content-Type", "text/csv; charset=utf-8")
	res, requestErr := u.client.Do(req)
	reader.Close()
	cancel()
	producerErr := <-done
	if res != nil {
		res.Body.Close()
	}
	// Даже timeout после PUT мог записать файл. Confirmation на следующем
	// invocation выполняется до нового PUT; сейчас используем оставшийся budget.
	if ctx.Err() != nil {
		return failure("disk_context_ended")
	}
	ok, confirmErr := u.confirmed(ctx, path, want)
	if ok {
		return nil
	}
	if confirmErr != nil {
		return confirmErr
	}
	if requestErr != nil {
		return failure("disk_network")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return responseError(res)
	}
	if producerErr != nil {
		return failure("csv_read_error")
	}
	return failure("disk_upload_unconfirmed")
}

package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rewinged/logging"
	"rewinged/models"
)

func TestMain(m *testing.M) {
	// Tests assert on behavior, not log output. Silence the global logger so
	// test output stays readable.
	logging.InitLogger("disable", true)
	os.Exit(m.Run())
}

// testPayload returns deterministic bytes plus their lowercase hex SHA-256,
// which is also the cache key and URL segment rewinged uses for installers.
func testPayload(content string) ([]byte, string) {
	data := []byte(content)
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// origin is a fake installer host. It counts hits so tests can prove rewinged
// downloads exactly once, and can hold its first response open so tests force a
// genuine race instead of accidentally measuring sequential cache hits.
type origin struct {
	server *httptest.Server

	attempts atomic.Int64
	started  chan struct{}
	release  chan struct{}
	once     sync.Once

	mu      sync.Mutex
	payload []byte
	status  int // 0 = serve payload, otherwise reply with this status
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	o := &origin{started: make(chan struct{}), release: make(chan struct{})}
	o.server = httptest.NewServer(http.HandlerFunc(o.handle))
	t.Cleanup(o.server.Close)
	return o
}

func (o *origin) handle(w http.ResponseWriter, r *http.Request) {
	n := o.attempts.Add(1)
	o.once.Do(func() { close(o.started) })

	// Only the first hit blocks. Tests that do not care about concurrency close
	// release up front, which turns this into a no-op.
	if n == 1 {
		<-o.release
	}

	o.mu.Lock()
	payload, status := o.payload, o.status
	o.mu.Unlock()

	if status != 0 {
		http.Error(w, "origin unavailable", status)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
	w.Write(payload)
}

func (o *origin) serve(p []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status = 0
	o.payload = p
}

func (o *origin) failWith(status int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status = status
}

func (o *origin) hits() int64 { return o.attempts.Load() }

// waitStarted blocks until the origin has been contacted at least once.
func (o *origin) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-o.started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for origin to be contacted")
	}
}

func newHandler(t *testing.T) (*InstallerDownloadHandler, string) {
	t.Helper()
	dir := t.TempDir()
	return NewInstallerDownloadHandler(dir), dir
}

// getInstaller requests an installer the way the router delivers it to the
// handler, i.e. after http.StripPrefix("/installers", ...) removed the prefix.
func getInstaller(h *InstallerDownloadHandler, tail string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/"+tail, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestStripPrefixContract pins the main.go wiring: the handler is mounted behind
// StripPrefix, so a bare /installers/ request must not become a listing.
func TestStripPrefixContract(t *testing.T) {
	h, dir := newHandler(t)
	_, sha := testPayload("cached installer")
	if err := os.WriteFile(filepath.Join(dir, sha), []byte("cached bytes"), 0o666); err != nil {
		t.Fatal(err)
	}

	mounted := http.StripPrefix("/installers", h)

	t.Run("prefixed-hit", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mounted.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/installers/"+sha, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "cached bytes" {
			t.Errorf("body = %q, want cached bytes", rec.Body.String())
		}
	})

	t.Run("no-directory-listing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mounted.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/installers/", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if strings.Contains(rec.Body.String(), sha) {
			t.Errorf("listing leaked cached sha: %q", rec.Body.String())
		}
	})
}

// TestInstallerDownloadColdCache is the core on-demand contract: nothing is on
// disk, the client request triggers the download, later requests are served
// from the cache.
func TestInstallerDownloadColdCache(t *testing.T) {
	h, dir := newHandler(t)
	payload, sha := testPayload("rewinged installer payload")

	o := newOrigin(t)
	o.serve(payload)
	close(o.release)
	models.InstallerSources.Set(sha, o.server.URL+"/installer.exe")

	rec := getInstaller(h, sha)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(payload) {
		t.Errorf("served body = %q, want %q", rec.Body.String(), payload)
	}
	if o.hits() != 1 {
		t.Errorf("origin hits = %d, want 1", o.hits())
	}

	cached := filepath.Join(dir, sha)
	b, err := os.ReadFile(cached)
	if err != nil {
		t.Fatalf("installer not cached at %s: %v", cached, err)
	}
	if string(b) != string(payload) {
		t.Errorf("cached content = %q, want %q", b, payload)
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Errorf("temp files left in store: %v", leftovers)
	}

	rec2 := getInstaller(h, sha)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", rec2.Code)
	}
	if o.hits() != 1 {
		t.Errorf("origin hits after cache hit = %d, want 1 (cache bypassed)", o.hits())
	}
}

// TestInstallerDownloadServesFromDiskWithoutOrigin proves the warm path needs no
// internet at all, and reuses files that already exist (previous run, or eager
// auto-internalization of some packages).
func TestInstallerDownloadServesFromDiskWithoutOrigin(t *testing.T) {
	h, dir := newHandler(t)
	payload, sha := testPayload("already on disk")
	if err := os.WriteFile(filepath.Join(dir, sha), payload, 0o666); err != nil {
		t.Fatal(err)
	}

	o := newOrigin(t)
	close(o.release)
	// deliberately NOT registered in InstallerSources: the file is enough

	rec := getInstaller(h, sha)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != string(payload) {
		t.Errorf("served body = %q, want %q", rec.Body.String(), payload)
	}
	if o.hits() != 0 {
		t.Errorf("origin hits = %d, want 0", o.hits())
	}
}

// TestInstallerDownloadUnknownSha covers installers never marked for
// internalization (e.g. skipped hosts): they must not be fetched or proxied.
func TestInstallerDownloadUnknownSha(t *testing.T) {
	h, _ := newHandler(t)
	_, sha := testPayload("unknown installer")

	o := newOrigin(t)
	close(o.release)

	rec := getInstaller(h, sha)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if o.hits() != 0 {
		t.Errorf("origin hits = %d, want 0 (must not proxy arbitrary URLs)", o.hits())
	}
}

// TestInstallerDownloadRejectsMaliciousPaths guards the store directory: the URL
// segment must be exactly a lowercase sha256 hex string, so traversal is
// structurally impossible rather than filtered.
func TestInstallerDownloadRejectsMaliciousPaths(t *testing.T) {
	h, dir := newHandler(t)
	_, sha := testPayload("guard target")

	if err := os.WriteFile(filepath.Join(dir, sha), []byte("secret installer bytes"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("do not read"), 0o666); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"dotdot-parent", "../secret.txt"},
		{"dotdot-system", "../etc/passwd"},
		{"double-encoded", "%2e%2e%2fsecret.txt"},
		{"uppercase-sha", strings.ToUpper(sha)},
		{"mixed-case-sha", sha[:32] + strings.ToUpper(sha[32:])},
		{"too-short", sha[:63]},
		{"too-long", sha + "00"},
		{"non-hex", strings.Repeat("z", 64)},
		{"empty", ""},
		{"nested", sha + "/extra"},
		{"dot", "."},
		{"dotdot", ".."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := getInstaller(h, tc.path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("path %q: status = %d, want 404", tc.path, rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(body, "secret") || strings.Contains(body, "root:") {
				t.Errorf("path %q: response leaked store contents: %q", tc.path, body)
			}
		})
	}
}

// TestInstallerDownloadOriginFailure: an erroring origin yields 502, caches
// nothing, and stays retryable instead of caching the failure.
func TestInstallerDownloadOriginFailure(t *testing.T) {
	h, dir := newHandler(t)
	payload, sha := testPayload("retryable installer")

	o := newOrigin(t)
	o.failWith(http.StatusNotFound)
	close(o.release)
	models.InstallerSources.Set(sha, o.server.URL+"/gone.exe")

	rec := getInstaller(h, sha)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("nothing should be cached on failure, found %v", got)
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Errorf("temp files left after failure: %v", leftovers)
	}

	// The failed entry must have been evicted so a later client retries
	// rather than being served a cached error forever.
	o.serve(payload)
	rec2 := getInstaller(h, sha)
	if rec2.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200 (failed download never retried)", rec2.Code)
	}
	if rec2.Body.String() != string(payload) {
		t.Errorf("retry body = %q, want %q", rec2.Body.String(), payload)
	}
	if o.hits() != 2 {
		t.Errorf("origin hits = %d, want 2 (one failure, one retry)", o.hits())
	}
}

// TestInstallerDownloadHashMismatch is the cache-poisoning guard: bytes that do
// not hash to the manifest-declared SHA-256 must never be cached or served.
func TestInstallerDownloadHashMismatch(t *testing.T) {
	h, dir := newHandler(t)
	good, sha := testPayload("the real installer bytes")
	tainted := []byte("corrupted or tampered installer bytes")

	o := newOrigin(t)
	o.serve(tainted)
	close(o.release)
	models.InstallerSources.Set(sha, o.server.URL+"/tainted.exe")

	rec := getInstaller(h, sha)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 on hash mismatch", rec.Code)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("tainted bytes were cached: %v", got)
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Errorf("temp files left after mismatch: %v", leftovers)
	}

	o.serve(good)
	rec2 := getInstaller(h, sha)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status after origin fixed = %d, want 200", rec2.Code)
	}
	if rec2.Body.String() != string(good) {
		t.Errorf("body = %q, want %q", rec2.Body.String(), good)
	}
}

// TestInstallerDownloadConcurrentSingleFlight is the race-condition requirement:
// several clients asking for the same uncached installer must cause exactly one
// origin download, and every one must receive complete bytes.
func TestInstallerDownloadConcurrentSingleFlight(t *testing.T) {
	h, dir := newHandler(t)
	payload, sha := testPayload(strings.Repeat("installer-bytes-", 4000))

	o := newOrigin(t)
	o.serve(payload)
	models.InstallerSources.Set(sha, o.server.URL+"/popular.exe")

	const clients = 16
	var wg sync.WaitGroup
	codes := make([]int, clients)
	bodies := make([]string, clients)

	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := getInstaller(h, sha)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}

	// Hold the origin on the first (and only permitted) download until the other
	// clients have piled up on the in-flight entry, so this is a real race and
	// not 16 sequential cache hits.
	o.waitStarted(t)
	time.Sleep(300 * time.Millisecond)
	close(o.release)
	wg.Wait()

	if o.hits() != 1 {
		t.Fatalf("origin hits = %d, want exactly 1", o.hits())
	}
	for i := 0; i < clients; i++ {
		if codes[i] != http.StatusOK {
			t.Errorf("client %d status = %d, want 200", i, codes[i])
		}
		if bodies[i] != string(payload) {
			t.Errorf("client %d got %d bytes, want %d (partial file served)", i, len(bodies[i]), len(payload))
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, sha)); err != nil || string(b) != string(payload) {
		t.Errorf("cached file invalid: err=%v len=%d", err, len(b))
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Errorf("temp files left after concurrent download: %v", leftovers)
	}
}

// TestInstallerDownloadConcurrentFailure proves a failing origin does not cause a
// stampede: concurrent clients share one attempt and can retry afterwards.
func TestInstallerDownloadConcurrentFailure(t *testing.T) {
	h, dir := newHandler(t)
	payload, sha := testPayload("origin was down")

	o := newOrigin(t)
	o.failWith(http.StatusServiceUnavailable)
	models.InstallerSources.Set(sha, o.server.URL+"/down.exe")

	const clients = 8
	var wg sync.WaitGroup
	codes := make([]int, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = getInstaller(h, sha).Code
		}(i)
	}
	o.waitStarted(t)
	time.Sleep(300 * time.Millisecond)
	close(o.release)
	wg.Wait()

	if o.hits() != 1 {
		t.Fatalf("origin hits = %d, want exactly 1 under failure", o.hits())
	}
	for i, c := range codes {
		if c != http.StatusBadGateway {
			t.Errorf("client %d status = %d, want 502", i, c)
		}
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("store must stay empty after failed concurrent download, found %v", got)
	}

	o.serve(payload)
	if rec := getInstaller(h, sha); rec.Code != http.StatusOK {
		t.Fatalf("status after recovery = %d, want 200", rec.Code)
	}
}

// TestInstallerDownloadRangeRequest guards the claim that cached installers are
// served with Range support, which winget relies on to resume downloads.
func TestInstallerDownloadRangeRequest(t *testing.T) {
	h, dir := newHandler(t)
	payload, sha := testPayload("0123456789abcdefghij")
	if err := os.WriteFile(filepath.Join(dir, sha), payload, 0o666); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+sha, nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != "2345" {
		t.Errorf("range body = %q, want %q", rec.Body.String(), "2345")
	}
}

// TestInstallerDownloadIndependence: a slow download of one installer must not
// block requests for a different installer.
func TestInstallerDownloadIndependence(t *testing.T) {
	h, dir := newHandler(t)
	p1, sha1 := testPayload("first installer")
	p2, sha2 := testPayload("second installer")

	slow := newOrigin(t)
	slow.serve(p1)
	models.InstallerSources.Set(sha1, slow.server.URL+"/slow.exe")

	fast := newOrigin(t)
	fast.serve(p2)
	close(fast.release)
	models.InstallerSources.Set(sha2, fast.server.URL+"/fast.exe")

	// sha1's download is held open by the slow origin
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = getInstaller(h, sha1)
	}()
	slow.waitStarted(t)

	rec := getInstaller(h, sha2)
	if rec.Code != http.StatusOK {
		t.Fatalf("unrelated installer status = %d, want 200 (blocked by another download)", rec.Code)
	}
	if rec.Body.String() != string(p2) {
		t.Errorf("body = %q, want %q", rec.Body.String(), p2)
	}

	close(slow.release)
	<-done
	if b, err := os.ReadFile(filepath.Join(dir, sha1)); err != nil || string(b) != string(p1) {
		t.Errorf("slow installer cached incorrectly: err=%v", err)
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func tmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, n := range files(t, dir) {
		if strings.HasSuffix(n, ".tmp") {
			out = append(out, n)
		}
	}
	return out
}

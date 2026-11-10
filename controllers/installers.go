package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"rewinged/logging"
	"rewinged/models"
)

// Internalized installer files are named with their SHA-256 in lowercase hex.
// Anything that is not exactly that (including "/" or "..") is rejected, which
// also makes directory listing and path traversal impossible on this route.
var installerShaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// download tracks one in-flight on-demand installer fetch so that concurrent
// requests for the same installer share a single download instead of racing to
// fetch it multiple times. err is written before done is closed, so it is safe
// to read for every goroutine that observes the close.
type download struct {
	done chan struct{}
	err  error
}

// InstallerDownloadHandler serves internalized installers from disk and, on a
// cache miss, downloads them from the origin URL remembered at manifest
// ingestion time, then serves them to this and all later requests.
type InstallerDownloadHandler struct {
	storePath string
	mu        sync.Mutex
	inFlight  map[string]*download
}

func NewInstallerDownloadHandler(storePath string) *InstallerDownloadHandler {
	return &InstallerDownloadHandler{
		storePath: storePath,
		inFlight:  make(map[string]*download),
	}
}

func (h *InstallerDownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// http.StripPrefix("/installers", ...) leaves a leading slash, e.g. "/<sha256>"
	sha := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if !installerShaRe.MatchString(sha) {
		http.NotFound(w, r)
		return
	}

	destFile := filepath.Join(h.storePath, sha)

	// Fast path: the installer is already on disk (downloaded on demand
	// earlier, or pre-populated by eager auto-internalization) - serve it.
	// http.ServeFile also handles Range / ETag / Last-Modified for winget.
	if _, err := os.Stat(destFile); err == nil {
		http.ServeFile(w, r, destFile)
		return
	}

	originURL, ok := models.InstallerSources.Get(sha)
	if !ok {
		// rewinged does not know this installer, so it was never marked for
		// internalization (skipped host or unknown sha)
		http.NotFound(w, r)
		return
	}

	if err := h.downloadInstaller(r.Context(), sha, originURL, destFile); err != nil {
		logging.Logger.Error().Err(err).Str("installer", sha).Msg("on-demand installer download failed")
		http.Error(w, "installer download failed", http.StatusBadGateway)
		return
	}

	http.ServeFile(w, r, destFile)
}

// downloadInstaller fetches the installer from its origin exactly once, even
// when multiple clients request it simultaneously. The first caller performs
// the fetch while the others wait on its result. A failed download is removed
// from the in-flight map so a later request can retry it.
func (h *InstallerDownloadHandler) downloadInstaller(ctx context.Context, sha, originURL, destFile string) error {
	h.mu.Lock()
	if d, ok := h.inFlight[sha]; ok {
		h.mu.Unlock()
		logging.Logger.Debug().Str("installer", sha).Msg("waiting for in-progress download by another client")
		select {
		case <-d.done:
			return d.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d := &download{done: make(chan struct{})}
	h.inFlight[sha] = d
	h.mu.Unlock()

	// Deliberately detached from the requesting client's context: if that
	// client disconnects mid-download, everyone else waiting on this fetch
	// still gets a complete file.
	err := h.fetchInstaller(sha, originURL, destFile)

	h.mu.Lock()
	if err != nil {
		delete(h.inFlight, sha)
	}
	h.mu.Unlock()

	d.err = err
	close(d.done)
	return err
}

func (h *InstallerDownloadHandler) fetchInstaller(sha, originURL, destFile string) error {
	logging.Logger.Info().Str("installer", sha).Str("url", originURL).Msg("downloading installer on demand")

	resp, err := http.Get(originURL)
	if err != nil {
		return fmt.Errorf("cannot download %s: %w", originURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cannot download %s: http status %d", originURL, resp.StatusCode)
	}

	// Download to a temp file in the same directory, then rename into place,
	// so concurrent readers never observe a partial file.
	tmp, err := os.CreateTemp(h.storePath, sha+".*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create temp file in %s: %w", h.storePath, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		// removes the temp file on failure; a no-op once renamed away
		os.Remove(tmpName)
	}()

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if err != nil {
		return fmt.Errorf("cannot save installer from %s: %w", originURL, err)
	}

	// The manifest-declared SHA-256 is also the cache key. Verify the bytes
	// match before trusting them, otherwise a corrupt or tampered download
	// would be served forever.
	if got := hex.EncodeToString(hasher.Sum(nil)); got != sha {
		return fmt.Errorf("installer sha mismatch: manifest says %s, download hashes to %s", sha, got)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cannot close temp file: %w", err)
	}
	if err := os.Rename(tmpName, destFile); err != nil {
		return fmt.Errorf("cannot move installer into place: %w", err)
	}

	logging.Logger.Info().Str("installer", sha).Msgf("on-demand internalization complete, %d bytes written", n)
	return nil
}

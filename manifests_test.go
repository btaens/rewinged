package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rewinged/logging"
	"rewinged/models"
)

func TestMain(m *testing.M) {
	logging.InitLogger("disable", true)
	os.Exit(m.Run())
}

// installer14 is a minimal models.API_InstallerInterface implementation used to
// drive internalizeInstallers without constructing a whole manifest.
func installer14(url, sha string) *models.API_Installer_1_4_0 {
	return &models.API_Installer_1_4_0{
		Architecture:   "x64",
		InstallerType:  "exe",
		InstallerUrl:   url,
		InstallerSha256: sha,
	}
}

func storedFiles(t *testing.T, dir string) []string {
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

// TestInternalizeInstallersEagerDownloadsAtStartup pins the existing behavior so
// the on-demand change cannot silently regress it.
func TestInternalizeInstallersEagerDownloadsAtStartup(t *testing.T) {
	payload := []byte("eagerly downloaded installer")
	o := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer o.Close()

	dir := t.TempDir()
	sha := "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	models.InternalizedInstallers.Set(sha, false)

	ins := []models.API_InstallerInterface{installer14(o.URL+"/x.exe", sha)}
	internalizeInstallers("Test.Eager", "1.0.0", ins, false, dir, nil)

	if got := storedFiles(t, dir); len(got) != 1 || got[0] != sha {
		t.Fatalf("eager mode should download to %s, store contains %v", sha, got)
	}
	b, err := os.ReadFile(filepath.Join(dir, sha))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(payload) {
		t.Errorf("downloaded content = %q, want %q", b, payload)
	}
	if !models.InternalizedInstallers.Get(sha) {
		t.Error("installer should be marked internalized in eager mode")
	}
}

// TestInternalizeInstallersOnDemandDefersDownload is the new behavior: at ingest
// nothing is downloaded, nothing is written to the store, but the origin URL is
// remembered and the installer is flagged so its URL can be rewritten.
func TestInternalizeInstallersOnDemandDefersDownload(t *testing.T) {
	var hits int
	o := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("must not be downloaded during ingest"))
	}))
	defer o.Close()

	dir := t.TempDir()
	sha := "b0fdeadbeef000000000000000000000000000000000000000000000000000ff"
	upper := strings.ToUpper(sha)
	models.InternalizedInstallers.Set(sha, false)

	ins := []models.API_InstallerInterface{installer14(o.URL+"/x.exe", upper)}
	internalizeInstallers("Test.OnDemand", "1.0.0", ins, true, dir, nil)

	if hits != 0 {
		t.Errorf("on-demand ingest hit the origin %d times, want 0", hits)
	}
	if got := storedFiles(t, dir); len(got) != 0 {
		t.Errorf("on-demand ingest wrote files to store: %v", got)
	}

	// origin must be remembered, keyed by the lowercase sha (the URL segment)
	gotURL, ok := models.InstallerSources.Get(sha)
	if !ok {
		t.Fatal("origin URL was not registered for on-demand download")
	}
	if gotURL != o.URL+"/x.exe" {
		t.Errorf("registered URL = %q, want %q", gotURL, o.URL+"/x.exe")
	}

	// the manifest stores the sha in its original (here uppercase) form, which is
	// the key the controller uses when deciding whether to rewrite the URL
	if !models.InternalizedInstallers.Get(upper) {
		t.Error("installer must be marked internalizable so its URL gets rewritten before the file exists")
	}
	// but the download cache key is always the lowercase sha, because that is the
	// form the /installers/<sha> URL uses
	if _, ok := models.InstallerSources.Get(sha); !ok {
		t.Error("InstallerSources must be keyed by the lowercase sha")
	}
}

// TestInternalizeInstallersOnDemandRespectsSkipHosts: skipped hostnames must not
// be internalized in either mode - their original URL has to survive untouched.
func TestInternalizeInstallersOnDemandRespectsSkipHosts(t *testing.T) {
	o := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("skipped host must never be contacted")
	}))
	defer o.Close()

	dir := t.TempDir()
	sha := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	models.InternalizedInstallers.Set(sha, false)

	// u.Hostname() excludes the port, so that is the form autoInternalizeSkip matches
	host := "127.0.0.1"
	ins := []models.API_InstallerInterface{installer14(o.URL+"/skipme.exe", sha)}
	internalizeInstallers("Test.Skip", "1.0.0", ins, true, dir, []string{host})

	if _, ok := models.InstallerSources.Get(sha); ok {
		t.Error("skipped host must not be registered for on-demand download")
	}
	if models.InternalizedInstallers.Get(sha) {
		t.Error("skipped host must not be marked internalized")
	}
	if got := storedFiles(t, dir); len(got) != 0 {
		t.Errorf("skipped host wrote files: %v", got)
	}
	// and the installer keeps pointing at its original URL
	if u := ins[0].GetInstallerUrl(); u != o.URL+"/skipme.exe" {
		t.Errorf("skipped installer URL changed to %q", u)
	}
}

// TestInternalizeInstallersOnDemandMalformedURL: an unparseable InstallerUrl must
// be skipped without panicking or registering a bogus source.
func TestInternalizeInstallersOnDemandMalformedURL(t *testing.T) {
	dir := t.TempDir()
	sha := "9988776655443322110099887766554433221100998877665544332211009988"
	models.InternalizedInstallers.Set(sha, false)

	ins := []models.API_InstallerInterface{installer14("http://[::1", sha)}
	internalizeInstallers("Test.Bad", "1.0.0", ins, true, dir, nil)

	if _, ok := models.InstallerSources.Get(sha); ok {
		t.Error("malformed URL must not be registered")
	}
	if models.InternalizedInstallers.Get(sha) {
		t.Error("malformed URL must not be marked internalized")
	}
}

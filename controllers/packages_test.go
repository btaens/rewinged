package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"rewinged/models"
)

// packageRouter mirrors main.go's routing so PathValue("package_identifier") is
// populated the way the real server provides it.
func packageRouter(h *GetPackageHandler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/packageManifests/{package_identifier}", http.HandlerFunc(h.GetPackage))
	return mux
}

// getManifest fetches a package manifest through the routed handler and returns
// the raw JSON so assertions can inspect the serialized InstallerUrl.
func getManifest(t *testing.T, h *GetPackageHandler, packageID string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	packageRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/packageManifests/"+packageID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// installerURLs pulls every InstallerUrl out of the serialized response.
func installerURLs(t *testing.T, body string) []string {
	t.Helper()
	var parsed struct {
		Data struct {
			Versions []struct {
				Installers []struct {
					InstallerUrl string `json:"InstallerUrl"`
				} `json:"Installers"`
			} `json:"Versions"`
		} `json:"Data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, body)
	}
	var urls []string
	for _, v := range parsed.Data.Versions {
		for _, in := range v.Installers {
			urls = append(urls, in.InstallerUrl)
		}
	}
	return urls
}

// TestGetPackageRewritesColdInstallerURL is the load-bearing invariant of
// on-demand internalization: an installer that has NOT been downloaded yet must
// still be advertised with a rewinged URL. If this regressed, clients on a
// network with no internet would receive origin URLs they cannot reach and
// on-demand downloading could never trigger.
func TestGetPackageRewritesColdInstallerURL(t *testing.T) {
	// the manifest declares the sha in uppercase, the way the community repo does
	upperSha := "A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F90"
	origURL := "https://releases.example.com/files/installer.exe"

	// NOT registered in InstallerSources and no file on disk: a cold installer.
	// internalizeInstallers only marked it internalizable.
	models.InternalizedInstallers.Set(upperSha, true)

	models.Manifests.Set("Test.Cold", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: origURL, InstallerSha256: upperSha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}
	body := getManifest(t, h, "Test.Cold")

	urls := installerURLs(t, body)
	if len(urls) != 1 {
		t.Fatalf("got %d installer URLs, want 1: %v", len(urls), urls)
	}
	want := "http://example.com/installers/" + strings.ToLower(upperSha)
	if urls[0] != want {
		t.Errorf("cold installer URL = %q, want %q (must be rewritten to rewinged before download)", urls[0], want)
	}
	if strings.Contains(urls[0], "releases.example.com") {
		t.Errorf("origin URL leaked to offline client: %q", urls[0])
	}
}

// TestGetPackageDoesNotRewriteSkippedInstaller: installers on skip-listed hosts
// keep their original URL, which is the whole point of autoInternalizeSkip.
func TestGetPackageDoesNotRewriteSkippedInstaller(t *testing.T) {
	sha := "000000000000000000000000000000000000000000000000000000000000dead"
	origURL := "https://internal.example.org/installer.exe"
	models.InternalizedInstallers.Set(sha, false)

	models.Manifests.Set("Test.Skipped", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "msi", InstallerUrl: origURL, InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}
	urls := installerURLs(t, getManifest(t, h, "Test.Skipped"))
	if len(urls) != 1 {
		t.Fatalf("got %d URLs, want 1", len(urls))
	}
	if urls[0] != origURL {
		t.Errorf("skipped installer URL = %q, want original %q", urls[0], origURL)
	}
}

// TestGetPackageNoInternalization proves the feature is off unless asked for:
// with internalization disabled every InstallerUrl is passed through untouched.
func TestGetPackageNoInternalization(t *testing.T) {
	sha := "111111111111111111111111111111111111111111111111111111111111beef"
	origURL := "https://releases.example.com/plain.exe"
	models.InternalizedInstallers.Set(sha, true) // marked, but feature disabled

	models.Manifests.Set("Test.NoInternalize", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: origURL, InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: false}
	urls := installerURLs(t, getManifest(t, h, "Test.NoInternalize"))
	if len(urls) != 1 || urls[0] != origURL {
		t.Errorf("URLs = %v, want original %q", urls, origURL)
	}
}

// TestGetPackageUsesForwardedHost pins the reverse-proxy case: behind a proxy the
// rewritten URL must use the externally visible host, or clients get an
// unreachable internal address.
func TestGetPackageUsesForwardedHost(t *testing.T) {
	sha := "222222222222222222222222222222222222222222222222222222222222cafe"
	models.InternalizedInstallers.Set(sha, true)

	models.Manifests.Set("Test.Proxied", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: "https://x.example.com/a.exe", InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}

	req := httptest.NewRequest(http.MethodGet, "/api/packageManifests/Test.Proxied", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "winget.internal.example")
	rec := httptest.NewRecorder()
	packageRouter(h).ServeHTTP(rec, req)

	urls := installerURLs(t, rec.Body.String())
	want := "https://winget.internal.example/installers/" + sha
	if len(urls) != 1 || urls[0] != want {
		t.Errorf("proxied URL = %v, want [%q]", urls, want)
	}
}

// TestGetPackageTlsScheme: with -https the rewritten URL must be https.
func TestGetPackageTlsScheme(t *testing.T) {
	sha := "333333333333333333333333333333333333333333333333333333333333b00b"
	models.InternalizedInstallers.Set(sha, true)

	models.Manifests.Set("Test.Tls", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: "https://x.example.com/a.exe", InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: true, InternalizationEnabled: true}
	urls := installerURLs(t, getManifest(t, h, "Test.Tls"))
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://") {
		t.Errorf("URLs = %v, want https scheme", urls)
	}
}

// TestGetPackageNotFound pins the winget API contract for unknown packages.
func TestGetPackageNotFound(t *testing.T) {
	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}
	rec := httptest.NewRecorder()
	packageRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/packageManifests/Test.Missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var e models.API_WingetApiError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body not JSON: %v (%q)", err, rec.Body.String())
	}
	if e.ErrorCode != 404 {
		t.Errorf("ErrorCode = %d, want 404", e.ErrorCode)
	}
}

// TestGetPackageRewriteIsIdempotent: the rewrite must be stable across repeated
// requests, since it mutates the stored manifest rather than a copy.
func TestGetPackageRewriteIsIdempotent(t *testing.T) {
	sha := "444444444444444444444444444444444444444444444444444444444444abcd"
	models.InternalizedInstallers.Set(sha, true)

	models.Manifests.Set("Test.Idempotent", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: "https://origin.example/z.exe", InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}
	first := installerURLs(t, getManifest(t, h, "Test.Idempotent"))
	second := installerURLs(t, getManifest(t, h, "Test.Idempotent"))
	third := installerURLs(t, getManifest(t, h, "Test.Idempotent"))

	want := "http://example.com/installers/" + sha
	for i, u := range [][]string{first, second, third} {
		if len(u) != 1 || u[0] != want {
			t.Errorf("request %d URL = %v, want [%q]", i+1, u, want)
		}
	}
}

// TestGetPackagePerRequestOrigin is a regression guard for the in-place rewrite:
// the stored manifest keeps whichever URL the last request wrote, so every
// response must be rewritten from its own request headers before it is
// serialized. If rewrite and marshal ever get decoupled, a client behind a
// proxy could be handed another client's host and fail to download.
func TestGetPackagePerRequestOrigin(t *testing.T) {
	sha := "666666666666666666666666666666666666666666666666666666666666ce01"
	models.InternalizedInstallers.Set(sha, true)

	models.Manifests.Set("Test.MixedOrigins", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: "https://origin.example/a.exe", InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}
	router := packageRouter(h)

	viaProxy := func() string {
		req := httptest.NewRequest(http.MethodGet, "/api/packageManifests/Test.MixedOrigins", nil)
		req.Host = "10.0.0.5:8080"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "winget.corp.example")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return installerURLs(t, rec.Body.String())[0]
	}

	viaDirect := func() string {
		req := httptest.NewRequest(http.MethodGet, "/api/packageManifests/Test.MixedOrigins", nil)
		req.Host = "direct.corp.example:8080"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return installerURLs(t, rec.Body.String())[0]
	}

	// interleave: each response must match the request that asked for it
	for i := 0; i < 5; i++ {
		if got := viaProxy(); got != "https://winget.corp.example/installers/"+sha {
			t.Fatalf("iteration %d proxied URL = %q, want the proxied origin", i, got)
		}
		if got := viaDirect(); got != "http://direct.corp.example:8080/installers/"+sha {
			t.Fatalf("iteration %d direct URL = %q, want the direct origin", i, got)
		}
	}
}

// TestGetPackageConcurrentRequests exercises the read path the way a real
// deployment does: multiple winget clients fetching the same package at once.
// Run under -race. GetPackage rewrites InstallerUrl in place on the stored
// manifest, so this test exists to expose that mutation racing with reads.
func TestGetPackageConcurrentRequests(t *testing.T) {
	sha := "555555555555555555555555555555555555555555555555555555555555aa11"
	models.InternalizedInstallers.Set(sha, true)

	models.Manifests.Set("Test.Race", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		Installers: []models.API_Installer_1_4_0{
			{Architecture: "x64", InstallerType: "exe", InstallerUrl: "https://origin.example/r.exe", InstallerSha256: sha},
		},
	})

	h := &GetPackageHandler{TlsEnabled: false, InternalizationEnabled: true}
	router := packageRouter(h)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/packageManifests/Test.Race", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		}()
	}
	wg.Wait()
}

// TestSearchAndListSmoke keeps the API surface honest alongside the installer
// changes: search results and the package list reflect the manifest store.
func TestSearchAndListSmoke(t *testing.T) {
	models.Manifests.Set("Example.Searchable", "1.0.0", &models.API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		DefaultLocale:  models.API_DefaultLocale_1_4_0{PackageName: "Searchable App", ShortDescription: "does things"},
	})

	rec := httptest.NewRecorder()
	SearchForPackage(rec, httptest.NewRequest(http.MethodPost, "/api/manifestSearch",
		strings.NewReader(`{"Query":{"KeyWord":"Searchable"}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Example.Searchable") {
		t.Errorf("search response missing package: %s", rec.Body.String())
	}

	// no results must be 204 per the winget REST contract
	rec2 := httptest.NewRecorder()
	SearchForPackage(rec2, httptest.NewRequest(http.MethodPost, "/api/manifestSearch",
		strings.NewReader(`{"Query":{"KeyWord":"zzzznothinghere"}}`)))
	if rec2.Code != http.StatusNoContent {
		t.Errorf("empty search status = %d, want 204", rec2.Code)
	}

	// malformed body must be a 400, not a panic
	rec3 := httptest.NewRecorder()
	SearchForPackage(rec3, httptest.NewRequest(http.MethodPost, "/api/manifestSearch",
		strings.NewReader(`{"not-a-field":true}`)))
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("bad body status = %d, want 400", rec3.Code)
	}

	lrec := httptest.NewRecorder()
	GetPackages(lrec, httptest.NewRequest(http.MethodGet, "/api/packages", nil))
	if lrec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", lrec.Code)
	}
	if !strings.Contains(lrec.Body.String(), "Example.Searchable") {
		t.Errorf("package list missing entry: %s", lrec.Body.String())
	}
}

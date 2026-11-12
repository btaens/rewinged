package models

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"rewinged/logging"
)

func TestMain(m *testing.M) {
	logging.InitLogger("disable", true)
	os.Exit(m.Run())
}

func TestInstallerSourcesSetGet(t *testing.T) {
	var s InstallerSourcesStore
	s.internal = make(map[string]string)

	if _, ok := s.Get("missing"); ok {
		t.Error("Get on empty store returned ok")
	}

	s.Set("aabb", "https://example.org/a.exe")
	got, ok := s.Get("aabb")
	if !ok || got != "https://example.org/a.exe" {
		t.Errorf("got (%q,%v), want (https://example.org/a.exe,true)", got, ok)
	}

	// last write wins: a re-ingested manifest replaces the stale origin
	s.Set("aabb", "https://example.org/a-v2.exe")
	if got, _ := s.Get("aabb"); got != "https://example.org/a-v2.exe" {
		t.Errorf("after overwrite got %q", got)
	}
}

func TestInstallerSourcesEmptyStringDistinguished(t *testing.T) {
	var s InstallerSourcesStore
	s.internal = make(map[string]string)
	s.Set("cc", "")

	// ok must be true even for an empty value, otherwise the handler would treat
	// a registered-but-empty URL as "unknown installer" (404 instead of error)
	if _, ok := s.Get("cc"); !ok {
		t.Error("registered empty URL reported as missing")
	}
}

func TestInternalizedInstallersStore(t *testing.T) {
	var s InternalizedInstallersStore
	s.internal = make(map[string]bool)

	if s.Get("nope") {
		t.Error("missing key reported internalized")
	}
	s.Set("x", true)
	if !s.Get("x") {
		t.Error("Set(true) not visible")
	}
	s.Set("x", false)
	if s.Get("x") {
		t.Error("Set(false) not visible - live reload could leave stale rewrites")
	}
}

func TestManifestsStoreBasics(t *testing.T) {
	var ms ManifestsStore
	ms.internal = make(map[string]map[string]manifestEntry)

	if n := ms.GetManifestCount(); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if got := ms.GetAllVersions("Test.None"); len(got) != 0 {
		t.Errorf("unknown package returned %d versions", len(got))
	}
	if got := ms.Get("Test.None", "1.0.0"); got != nil {
		t.Errorf("unknown package returned %v", got)
	}

	v1 := &API_ManifestVersion_1_4_0{PackageVersion: "1.0.0"}
	v2 := &API_ManifestVersion_1_4_0{PackageVersion: "2.0.0"}
	ms.Set("Test.Multi", "1.0.0", v1)
	ms.Set("Test.Multi", "2.0.0", v2)
	ms.Set("Test.Other", "1.0.0", v1)

	if n := ms.GetManifestCount(); n != 2 {
		t.Errorf("GetManifestCount = %d, want 2 (counts packages, not versions)", n)
	}
	if got := ms.GetAllVersions("Test.Multi"); len(got) != 2 {
		t.Errorf("GetAllVersions = %d, want 2", len(got))
	}
	if got := ms.Get("Test.Multi", "2.0.0"); got == nil || got.GetPackageVersion() != "2.0.0" {
		t.Errorf("Get returned %v", got)
	}

	// re-setting a version replaces rather than duplicates it
	ms.Set("Test.Multi", "2.0.0", &API_ManifestVersion_1_4_0{PackageVersion: "2.0.0"})
	if got := ms.GetAllVersions("Test.Multi"); len(got) != 2 {
		t.Errorf("after re-set GetAllVersions = %d, want 2", len(got))
	}

	ids := ms.GetAllPackageIdentifiers()
	if len(ids) != 2 {
		t.Fatalf("GetAllPackageIdentifiers = %d, want 2", len(ids))
	}
	var names []string
	for _, p := range ids {
		names = append(names, p.PackageIdentifier)
	}
	for _, want := range []string{"Test.Multi", "Test.Other"} {
		var found bool
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("identifier %q missing from %v", want, names)
		}
	}
}

// TestManifestsStoreConcurrentAccess exercises the store the way the server does
// at runtime: 6 ingest workers writing while API handlers read. Run with -race;
// this is the test that catches the unsynchronized manifest access.
func TestManifestsStoreConcurrentAccess(t *testing.T) {
	var ms ManifestsStore
	ms.internal = make(map[string]map[string]manifestEntry)

	var wg sync.WaitGroup
	const writers = 6
	const perWriter = 50

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("Test.Pkg%d", w)
				ms.Set(id, fmt.Sprintf("1.0.%d", i), &API_ManifestVersion_1_4_0{PackageVersion: fmt.Sprintf("1.0.%d", i)})
			}
		}(w)
	}

	// readers hammer every read path while writes are in flight
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				ms.GetAllVersions("Test.Pkg1")
				ms.Get("Test.Pkg2", "1.0.3")
				_ = ms.GetManifestCount()
				ms.GetAllPackageIdentifiers()
				ms.GetByKeyword("anything")
			}
		}()
	}

	wg.Wait()

	if n := ms.GetManifestCount(); n != writers {
		t.Errorf("package count = %d, want %d", n, writers)
	}
	if got := ms.GetAllVersions("Test.Pkg0"); len(got) != perWriter {
		t.Errorf("versions for Test.Pkg0 = %d, want %d (lost writes)", len(got), perWriter)
	}
}

func TestInstallerSourcesConcurrentAccess(t *testing.T) {
	var s InstallerSourcesStore
	s.internal = make(map[string]string)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := fmt.Sprintf("%02x%02x", w, i)
				s.Set(key, "https://example.org/"+key)
				s.Get(key)
			}
		}(w)
	}
	wg.Wait()

	for w := 0; w < 8; w++ {
		for i := 0; i < 200; i++ {
			key := fmt.Sprintf("%02x%02x", w, i)
			if got, ok := s.Get(key); !ok || !strings.HasSuffix(got, key) {
				t.Fatalf("lost or corrupted entry %q: (%q,%v)", key, got, ok)
			}
		}
	}
}

func TestGetByKeywordAndFiltersSmoke(t *testing.T) {
	var ms ManifestsStore
	ms.internal = make(map[string]map[string]manifestEntry)

	ms.Set("Example.Laptop", "1.0.0", &API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		DefaultLocale:  API_DefaultLocale_1_4_0{PackageName: "Lapce Editor", ShortDescription: "A light editor"},
	})
	ms.Set("Example.Other", "1.0.0", &API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		DefaultLocale:  API_DefaultLocale_1_4_0{PackageName: "Something Else", ShortDescription: "unrelated"},
	})

	if res := ms.GetByKeyword("lapce"); len(res) != 1 {
		t.Errorf("keyword search returned %d results, want 1", len(res))
	}
	if res := ms.GetByKeyword("LAPCE"); len(res) != 1 {
		t.Errorf("case-insensitive keyword search returned %d results, want 1", len(res))
	}
	if res := ms.GetByKeyword("zzzz"); len(res) != 0 {
		t.Errorf("no-match keyword returned %d results, want 0", len(res))
	}

	res, unsupported := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{{
			PackageMatchField: "PackageIdentifier",
			RequestMatch:      API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "Example.Laptop"},
		}},
		nil,
	)
	if len(res) != 1 {
		t.Errorf("filter search returned %d results, want 1", len(res))
	}
	if len(unsupported) != 0 {
		t.Errorf("unsupported fields = %v, want none", unsupported)
	}
}

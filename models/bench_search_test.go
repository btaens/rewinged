package models

// Search-path performance guards.
//
// These do NOT run under `go test` (CI runs `go test -race ./...` and
// `go test ./...`, neither enables -bench), so they cost nothing in CI while
// staying type-checked. Run them with:
//
//	go test ./models/ -run '^$' -bench 'Search' -benchtime=20x
//
// The store sizes mirror what the correlation path has to scan: `winget list`
// sends one ProductCode + NormalizedPackageNameAndPublisher inclusion body per
// installed application, and before the derived values were precomputed at
// ingestion every one of those requests re-normalized every PackageName in the
// store (~21us and 32 allocations per version-doc, measured on a Xeon E5-2650
// v4). The pair-inclusion benchmark is the load-bearing one - it is what makes
// a single correlation query take seconds on a community-repo-sized store - so
// the numbers below are the regression to watch, not the absolute times.

import (
	"fmt"
	"testing"
)

func benchGuid(i int) string {
	return fmt.Sprintf("{%08X-1111-4222-8333-444455556666}", i)
}

// benchStore builds a store of many packages whose names all normalize to the
// same keyword: bracket-decorated and carrying a dotted version token, which is
// exactly the shape ("Mozilla Firefox (en-US)" and its locale siblings,
// "Microsoft .NET SDK 8.0" and its majors) that makes one correlation request
// fan out over the whole store. The fan-out is deliberate - it exercises the
// result assembly and the disambiguation rules, not just the scan.
func benchStore(pkgs, versions int) *ManifestsStore {
	ms := &ManifestsStore{}
	ms.internal = make(map[string]map[string]manifestEntry, pkgs)
	for i := 0; i < pkgs; i++ {
		for j := 0; j < versions; j++ {
			version := fmt.Sprintf("1.%d.%d", j, i%97)
			ms.Set(fmt.Sprintf("Contoso.App%d", i), version, &API_ManifestVersion_1_10_0{
				PackageVersion: version,
				DefaultLocale: API_DefaultLocale_1_10_0{
					// The dotted version token is stripped by
					// normalizePackageName and the bracket group removed, so every
					// package here reduces to "contosoawesometool".
					PackageName:      fmt.Sprintf("Contoso Awesome Tool %s (x64 en-US)", version),
					Publisher:        "Contoso",
					ShortDescription: "a tool that does things for developers and administrators",
				},
				Installers: []API_Installer_1_10_0{
					{
						Architecture:      "x64",
						InstallerType:     "msi",
						ProductCode:       benchGuid(i*100 + j),
						PackageFamilyName: fmt.Sprintf("Contoso.App%d_1234567890abc", i),
						AppsAndFeaturesEntries: []struct {
							DisplayName    string `yaml:"DisplayName" json:",omitempty"`
							Publisher      string `yaml:"Publisher" json:",omitempty"`
							DisplayVersion string `yaml:"DisplayVersion" json:",omitempty"`
							ProductCode    string `yaml:"ProductCode" json:",omitempty"`
							UpgradeCode    string `yaml:"UpgradeCode" json:",omitempty"`
							InstallerType  string `yaml:"InstallerType" json:",omitempty"`
						}{{
							DisplayName:    "Contoso Awesome Tool",
							DisplayVersion: version,
							UpgradeCode:    benchGuid(i),
						}},
					},
					{
						Architecture:  "x86",
						InstallerType: "msi",
						ProductCode:   benchGuid(i*100 + j + 50),
					},
				},
			})
		}
	}
	return ms
}

// pairInclusion is the NormalizedPackageNameAndPublisher half of the exact
// inclusion body `winget list` sends. It matches every version of every package
// in the fixture, which is the worst case: full scan, full result assembly, and
// a fan-out the disambiguation rules then have to resolve.
func pairInclusion() API_SearchRequestPackageMatchFilter_1_1_0 {
	return API_SearchRequestPackageMatchFilter_1_1_0{
		PackageMatchField: "NormalizedPackageNameAndPublisher",
		RequestMatch:      API_SearchRequestMatch_1_1_0{KeyWord: "contosoawesometool", MatchType: "Exact"},
	}
}

func benchSearch(b *testing.B, ms *ManifestsStore, inclusions []API_SearchRequestPackageMatchFilter_1_1_0) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ms.GetByMatchFilter(inclusions, nil)
	}
}

func BenchmarkSearchPairInclusion1000x4(b *testing.B) {
	benchSearch(b, benchStore(1000, 4), []API_SearchRequestPackageMatchFilter_1_1_0{pairInclusion()})
}

func BenchmarkSearchPairInclusion4000x4(b *testing.B) {
	benchSearch(b, benchStore(4000, 4), []API_SearchRequestPackageMatchFilter_1_1_0{pairInclusion()})
}

// correlationBody is the full `winget list` request shape: an unanchored
// ProductCode (matching nothing) OR-ed with the normalized pair, which is the
// shape that fans out and then runs the disambiguation rules.
func BenchmarkSearchCorrelationBody4000x4(b *testing.B) {
	ms := benchStore(4000, 4)
	inclusions := []API_SearchRequestPackageMatchFilter_1_1_0{
		{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{
			KeyWord: "{00000000-1111-4222-8333-444455556666}", MatchType: "Exact"}},
		pairInclusion(),
	}
	benchSearch(b, ms, inclusions)
}

// Ingestion cost, the other side of the trade: deriving the values must not
// make startup noticeably slower.
func BenchmarkIngest1000x4(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchStore(1000, 4)
	}
}

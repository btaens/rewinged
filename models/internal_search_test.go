package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression guards for the installed-program correlation failures tracked as
// https://github.com/microsoft/winget-cli-restsource/issues/166:
//
// The winget client (CompositeSource.cpp) builds its `winget list` and
// post-install verification searches from ARP values that it runs through
// FoldCase, but sends every correlation filter with MatchType Exact, while
// real manifest GUIDs are uppercase. The old matching code additionally
// resolved ProductCode/PackageFamilyName via findField, whose first-match-wins
// scalar lookup never included the AppsAndFeaturesEntries GUIDs that are part
// of the client's correlation set.

func correlationStore(t *testing.T) *ManifestsStore {
	t.Helper()
	ms := &ManifestsStore{}
	ms.internal = make(map[string]map[string]API_ManifestVersionInterface)

	ms.Set("Example.App", "1.0.0", &API_ManifestVersion_1_4_0{
		PackageVersion: "1.0.0",
		DefaultLocale: API_DefaultLocale_1_4_0{
			PackageName:      "Fiddler",
			Publisher:        "Telerik",
			ShortDescription: "web debugger",
		},
		Installers: []API_Installer_1_4_0{
			{
				// real manifest GUIDs are uppercase and contain hex letters
				ProductCode:       "{AB12CD34-5678-9ABC-DEF0-123456789ABC}",
				PackageFamilyName: "ExampleApp_1234567890abc",
				AppsAndFeaturesEntries: []struct {
					DisplayName    string `yaml:"DisplayName" json:",omitempty"`
					Publisher      string `yaml:"Publisher" json:",omitempty"`
					DisplayVersion string `yaml:"DisplayVersion" json:",omitempty"`
					ProductCode    string `yaml:"ProductCode" json:",omitempty"`
					UpgradeCode    string `yaml:"UpgradeCode" json:",omitempty"`
					InstallerType  string `yaml:"InstallerType" json:",omitempty"`
				}{
					{
						DisplayName:    "Fiddler",
						Publisher:      "Telerik",
						DisplayVersion: "6.8.0",
						ProductCode:    "{FEE1DAD0-BEEF-4C0DE-8E5A-1234567890AB}",
						UpgradeCode:    "{DEADBE00-1111-2222-3333-444455556666}",
					},
				},
			},
		},
	})
	return ms
}

func TestProductCodeCorrelationSearchMatchesFoldedKeyword(t *testing.T) {
	ms := correlationStore(t)

	// Exactly what the client sends for an installed MSI: lowercase GUID, MatchType Exact.
	res, unsupported := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{{
			PackageMatchField: "ProductCode",
			RequestMatch:      API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{ab12cd34-5678-9abc-def0-123456789abc}"},
		}},
		nil,
	)
	if len(res) != 1 {
		t.Errorf("fold-case client keyword matched %d packages, want 1", len(res))
	}

	// The AppsAndFeaturesEntry ProductCode is a correlation identifier too
	// (winget-cli Manifest::GetProductCodes reads installer AND ARP entries).
	res, _ = ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{{
			PackageMatchField: "ProductCode",
			RequestMatch:      API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{fee1dad0-beef-4c0de-8e5a-1234567890ab}"},
		}},
		nil,
	)
	if len(res) != 1 {
		t.Errorf("ARP ProductCode keyword matched %d packages, want 1", len(res))
	}

	// A genuinely different GUID must still not match.
	res, _ = ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{{
			PackageMatchField: "ProductCode",
			RequestMatch:      API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{00000000-0000-0000-0000-000000000000}"},
		}},
		nil,
	)
	if len(res) != 0 {
		t.Errorf("foreign ProductCode matched %d packages, want 0", len(res))
	}
	if len(unsupported) != 0 {
		t.Errorf("unsupported fields = %v, want none", unsupported)
	}
}

// TestSearchFieldTeethCheck pins the premise of the fix: with the old logic,
// ProductCode resolved via findField to the first scalar string field of that
// name (an installer-level value) and was compared with a literal != under
// MatchType Exact. A fold-cased client keyword therefore could not match an
// uppercase manifest GUID, and ARP-entry GUIDs (only present under
// AppsAndFeaturesEntries) were never part of the comparison at all.
func TestSearchFieldTeethCheck(t *testing.T) {
	ms := correlationStore(t)
	versions := ms.GetAllVersions("Example.App")
	if len(versions) != 1 {
		t.Fatalf("fixture store broken: %d versions", len(versions))
	}

	// old code path, reproduced:
	f := findField(versions[0], "ProductCode")
	oldValue := f.String()
	clientKeyword := "{ab12cd34-5678-9abc-def0-123456789abc}" // folded, as the client sends it
	if oldValue == clientKeyword {
		t.Fatal("the old findField+Exact path now matches the folded keyword: the premise of the fix is wrong")
	}
	if oldValue != "{AB12CD34-5678-9ABC-DEF0-123456789ABC}" {
		t.Fatalf("findField resolved ProductCode to %q, expected the installer-level GUID", oldValue)
	}
	if strings.Contains(oldValue, "FEE1DAD0") {
		t.Fatal("ARP ProductCode unexpectedly reachable via findField's first match")
	}
}

func TestPackageFamilyNameCorrelationSearch(t *testing.T) {
	ms := correlationStore(t)

	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{{
			PackageMatchField: "PackageFamilyName",
			RequestMatch:      API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "exampleapp_1234567890abc"},
		}},
		nil,
	)
	if len(res) != 1 {
		t.Errorf("folded PackageFamilyName matched %d packages, want 1", len(res))
	}
}

func TestUnsupportedPackageMatchFieldsReported(t *testing.T) {
	ms := correlationStore(t)

	// Tag used to be routed through findField, which never resolves the plural
	// Tags field, so a Tag filter silently matched nothing. It must now be
	// reported instead.
	_, unsupported := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{ab12cd34-5678-9abc-def0-123456789abc}"}},
			{PackageMatchField: "Tag", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "whatever"}},
		},
		nil,
	)
	if len(unsupported) != 1 || unsupported[0] != "Tag" {
		t.Errorf("unsupported = %v, want [Tag]", unsupported)
	}
}

func TestUpgradeCodeMatchesARPEntries(t *testing.T) {
	ms := correlationStore(t)

	res, unsupported := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{{
			PackageMatchField: "UpgradeCode",
			RequestMatch:      API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{deadbe00-1111-2222-3333-444455556666}"},
		}},
		nil,
	)
	if len(res) != 1 {
		t.Errorf("folded UpgradeCode keyword matched %d packages, want 1", len(res))
	}
	if len(unsupported) != 0 {
		t.Errorf("UpgradeCode reported unsupported: %v", unsupported)
	}
}

func TestInclusionsRemainLogicalOR(t *testing.T) {
	ms := correlationStore(t)

	// An inclusion that does not match must not sink a package that matches another inclusion.
	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "nope"}},
			{PackageMatchField: "PackageIdentifier", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "Example.App"}},
		},
		nil,
	)
	if len(res) != 1 {
		t.Errorf("OR semantics: matched %d packages, want 1", len(res))
	}

	// Filters stay logical AND: a non-matching filter excludes the package.
	res, _ = ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "PackageIdentifier", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "Example.App"}},
		},
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "nope"}},
		},
	)
	if len(res) != 0 {
		t.Errorf("AND semantics: matched %d packages, want 0", len(res))
	}
}

func TestUnimplementedMatchTypeNoLongerPasses(t *testing.T) {
	ms := correlationStore(t)

	// The old switch fell through (empty default) so an unknown MatchType
	// accepted every package. It must now not match, so the server never
	// silently answers with results for a comparison it does not implement.
	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Wildcard", KeyWord: "*"}},
		},
		nil,
	)
	if len(res) != 0 {
		t.Errorf("Wildcard match accepted %d packages, want 0 (unimplemented)", len(res))
	}
}

func TestCorrelationGettersFeedSearchResponse(t *testing.T) {
	ms := correlationStore(t)
	version := ms.Get("Example.App", "1.0.0")

	if got := version.GetInstallerProductCodes(); len(got) != 2 {
		t.Errorf("GetInstallerProductCodes = %v, want installer + ARP entry codes", got)
	}
	if got := version.GetInstallerUpgradeCodes(); len(got) != 1 || got[0] != "{DEADBE00-1111-2222-3333-444455556666}" {
		t.Errorf("GetInstallerUpgradeCodes = %v", got)
	}
	if got := version.GetAppsAndFeaturesEntryVersions(); len(got) != 1 || got[0] != "6.8.0" {
		t.Errorf("GetAppsAndFeaturesEntryVersions = %v", got)
	}
	if got := version.GetInstallerPackageFamilyNames(); len(got) != 1 || got[0] != "ExampleApp_1234567890abc" {
		t.Errorf("GetInstallerPackageFamilyNames = %v", got)
	}

	// A version whose installers carry no optional identifiers must yield empty
	// (not null) slices, matching the 1.1.0 handler's old `[]string{}` behavior.
	bare := &API_ManifestVersion_1_4_0{PackageVersion: "1.0.0", Installers: []API_Installer_1_4_0{{ProductCode: ""}}}
	for name, got := range map[string][]string{
		"ProductCodes":                 bare.GetInstallerProductCodes(),
		"PackageFamilyNames":           bare.GetInstallerPackageFamilyNames(),
		"UpgradeCodes":                 bare.GetInstallerUpgradeCodes(),
		"AppsAndFeaturesEntryVersions": bare.GetAppsAndFeaturesEntryVersions(),
	} {
		if got == nil || len(got) != 0 {
			t.Errorf("%s = %v, want empty non-nil slice", name, got)
		}
	}
}

func TestSearchResponseWireShape(t *testing.T) {
	ms := correlationStore(t)
	version := ms.Get("Example.App", "1.0.0")

	sv := API_ManifestSearchVersion_1_4_0{
		PackageVersion:               version.GetPackageVersion(),
		PackageFamilyNames:           version.GetInstallerPackageFamilyNames(),
		ProductCodes:                 version.GetInstallerProductCodes(),
		AppsAndFeaturesEntryVersions: version.GetAppsAndFeaturesEntryVersions(),
		UpgradeCodes:                 version.GetInstallerUpgradeCodes(),
	}
	body, err := json.Marshal(API_ManifestSearchResult[API_ManifestSearchVersion_1_4_0]{
		Data: []API_ManifestSearchResponse[API_ManifestSearchVersion_1_4_0]{
			{PackageIdentifier: "Example.App", PackageName: "Fiddler", Publisher: "Telerik", Versions: []API_ManifestSearchVersion_1_4_0{sv}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The client (SearchResponseDeserializer_1_4) reads exactly these keys.
	for _, want := range []string{
		`"UpgradeCodes":["{DEADBE00-1111-2222-3333-444455556666}"]`,
		`"AppsAndFeaturesEntryVersions":["6.8.0"]`,
		`"ProductCodes":[`,
		`"PackageFamilyNames":["ExampleApp_1234567890abc"]`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("wire body missing %s\nbody: %s", want, body)
		}
	}
}

// The NormalizedPackageNameAndPublisher keyword is what makes `winget list`
// show a newer version at all. CompositeSource.cpp sends the correlation pair
// alongside the ARP ProductCode; the client's NameNormalization strips every
// character outside [\p{L}\p{Nd}] from the manifest name, so a name containing
// punctuation ("draw.io") arrives as "drawio". Comparing the raw manifest name
// against that keyword matches nothing, the ProductCode inclusion then matches
// only the single installed version (wix GUIDs change per version), and the
// client's latest-of-the-returned-versions is the installed one - so no
// "Available" upgrade is displayed.
//
// Keywords below are copied from a live winget 1.29.380 correlation request.
func normalizedPairStore(t *testing.T) *ManifestsStore {
	t.Helper()
	ms := &ManifestsStore{}
	ms.internal = make(map[string]map[string]API_ManifestVersionInterface)

	for _, tc := range []struct{ version, productCode string }{
		{"31.4.5", "{48AA8CF8-AC09-4435-AA45-43FFDFE27194}"},
		{"31.5.2", "{760E4075-E6CF-465E-82F4-6D727ADAD52D}"},
		{"32.6.10", "{CD86C383-8B30-45BF-B556-3842870F3295}"},
	} {
		ms.Set("JGraph.Draw", tc.version, &API_ManifestVersion_1_10_0{
			PackageVersion: tc.version,
			DefaultLocale: API_DefaultLocale_1_10_0{
				PackageName:      "draw.io",
				Publisher:        "JGraph",
				ShortDescription: "diagramming tool",
			},
			Installers: []API_Installer_1_10_0{{ProductCode: tc.productCode}},
		})
	}
	return ms
}

func TestNormalizedPackageNamePublisherMatchesPunctuatedName(t *testing.T) {
	ms := normalizedPairStore(t)

	// Exactly the inclusion the client sends for an installed draw.io.
	res, unsupported := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{48aa8cf8-ac09-4435-aa45-43ffdfe27194}"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "drawio"}},
		},
		nil,
	)
	if len(unsupported) != 0 {
		t.Errorf("unsupported fields = %v, want none", unsupported)
	}
	if len(res) != 1 {
		t.Fatalf("pair inclusion matched %d packages, want 1", len(res))
	}
	// The pair must carry every version, not just the installed one: that is
	// what lets the client report a newer Available version.
	if got := len(res["JGraph.Draw"]); got != 3 {
		t.Errorf("pair inclusion returned %d versions, want all 3", got)
	}

	// The pair alone (no ProductCode hit) must also match.
	res, _ = ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "drawio"}},
		},
		nil,
	)
	if got := len(res["JGraph.Draw"]); got != 3 {
		t.Errorf("pair alone matched %d versions, want 3", got)
	}

	// A different name must still not match.
	res, _ = ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "firefox"}},
		},
		nil,
	)
	if len(res) != 0 {
		t.Errorf("foreign normalized name matched %d packages, want 0", len(res))
	}
}

func TestNormalizePackageNameCharacterClass(t *testing.T) {
	// The client keeps Unicode letters and digits and removes everything else;
	// the accented case is from the same live log (a Hungarian ARP display name
	// arrives with á/ő intact), so an ASCII-only filter would break it.
	for _, tc := range []struct{ in, want string }{
		{"draw.io", "drawio"},
		{"Mozilla Firefox", "mozillafirefox"},
		{"7-Zip", "7zip"},
		{"WinSSH-Pageant", "winsshpageant"},
		{"Intel(R) Graphics Software & Drivers", "intelgraphicssoftwaredrivers"},
		{"Microsoft PC állapotellenőrzés", "microsoftpcállapotellenőrzés"},
		{"C++ Builder", "cbuilder"},
		{"Éditeur #1", "éditeur1"},
	} {
		if got := normalizePackageName(tc.in); got != tc.want {
			t.Errorf("normalizePackageName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

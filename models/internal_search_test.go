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
	// Inputs and expected outputs are taken from the live winget 1.29.380
	// correlation log: the client keeps Unicode letters and digits, drops
	// bracket-enclosed groups, and strips dotted version tokens. "windows11…"
	// pins that a LONE number survives (VersionDelimited requires a
	// punctuation-joined digit group), while "sdk 8.0" and "v14" must be
	// stripped.
	for _, tc := range []struct{ in, want string }{
		{"draw.io", "drawio"},
		{"Microsoft .NET SDK 8.0", "microsoftnetsdk"},
		{"Microsoft .NET Windows Desktop Runtime 10.0", "microsoftnetwindowsdesktopruntime"},
		{"Microsoft Visual C++ v14 Redistributable (x64)", "microsoftvisualcredistributable"},
		{"Windows 11 Installation Assistant", "windows11installationassistant"},
		{"Microsoft Office LTSC Professional Plus 2024", "microsoftofficeltscprofessionalplus2024"},
		{"Mozilla Firefox", "mozillafirefox"},
		{"7-Zip", "7zip"},
		{"WinSSH-Pageant", "winsshpageant"},
		{"Intel(R) Graphics Software & Drivers", "intelgraphicssoftwaredrivers"},
		{"Microsoft PC állapotellenőrzés", "microsoftpcállapotellenőrzés"},
		{"C++ Builder", "cbuilder"},
		{"Éditeur #1", "éditeur1"},
		{"Some App (KB5034441)", "someappkb5034441"},
		// Locale/channel variants collapse onto the base name. These are the
		// collisions that make one correlation search return several packages, so
		// they are pinned deliberately: the collapse in GetByMatchFilter, not the
		// normalizer, is where they get resolved.
		{"Mozilla Firefox (hu)", "mozillafirefox"},
		{"Microsoft .NET SDK 10.0", "microsoftnetsdk"},
	} {
		if got := normalizePackageName(tc.in); got != tc.want {
			t.Errorf("normalizePackageName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Second live-log regression: Microsoft.DotNet.SDK.8 uses burn installers with
// per-release GUIDs and a PackageName carrying a version token
// ("Microsoft .NET SDK 8.0" -> client KeyWord "microsoftnetsdk"). Before the
// token strip the pair inclusion matched zero versions and the response
// contained only the installed release, so `winget list` never offered the
// newer one.
func TestSDKStyleVersionTokenInNameStillCorrelates(t *testing.T) {
	ms := &ManifestsStore{}
	ms.internal = make(map[string]map[string]API_ManifestVersionInterface)
	for _, tc := range []struct{ version, productCode string }{
		{"8.0.425", "{AB39B1D0-4847-4C0D-A1C1-B2BE697F2B76}"},
		{"8.0.428", "{D76560AF-3660-42AF-8CEB-47FC58A70915}"},
	} {
		ms.Set("Microsoft.DotNet.SDK.8", tc.version, &API_ManifestVersion_1_10_0{
			PackageVersion: tc.version,
			DefaultLocale: API_DefaultLocale_1_10_0{
				PackageName:      "Microsoft .NET SDK 8.0",
				Publisher:        "Microsoft Corporation",
				ShortDescription: ".NET SDK",
			},
			Installers: []API_Installer_1_10_0{{ProductCode: tc.productCode}},
		})
	}

	// Exact wire body from the log for the installed 8.0.425.
	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{ab39b1d0-4847-4c0d-a1c1-b2be697f2b76}"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "microsoftnetsdk"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "microsoftnetsdk(x64)"}},
		},
		nil,
	)
	if got := len(res["Microsoft.DotNet.SDK.8"]); got != 2 {
		t.Errorf("SDK correlation returned %d versions, want both 2 (so 8.0.428 shows as Available)", got)
	}
	if got := len(res); got != 1 {
		t.Errorf("SDK correlation matched %d packages, want 1", got)
	}
}

// sdkFamilyStore reproduces the community repo at scale: every .NET SDK major is
// a separate PackageIdentifier whose PackageName carries a version token, so
// once normalizePackageName strips tokens the way the client does, all of them
// normalize to the identical keyword "microsoftnetsdk".
func sdkFamilyStore(t *testing.T) *ManifestsStore {
	t.Helper()
	ms := &ManifestsStore{}
	ms.internal = make(map[string]map[string]API_ManifestVersionInterface)

	for _, tc := range []struct{ id, version, name, productCode string }{
		{"Microsoft.DotNet.SDK.8", "8.0.425", "Microsoft .NET SDK 8.0", "{AB39B1D0-4847-4C0D-A1C1-B2BE697F2B76}"},
		{"Microsoft.DotNet.SDK.8", "8.0.428", "Microsoft .NET SDK 8.0", "{D76560AF-3660-42AF-8CEB-47FC58A70915}"},
		{"Microsoft.DotNet.SDK.9", "9.0.100", "Microsoft .NET SDK 9.0", "{40819C02-3BD5-4E8B-9C10-101EF13DE6A1}"},
		{"Microsoft.DotNet.SDK.10", "10.0.100", "Microsoft .NET SDK 10.0", "{0898FFD1-9871-4809-B4F9-09608F700177}"},
	} {
		ms.Set(tc.id, tc.version, &API_ManifestVersion_1_10_0{
			PackageVersion: tc.version,
			DefaultLocale: API_DefaultLocale_1_10_0{
				PackageName:      tc.name,
				Publisher:        "Microsoft Corporation",
				ShortDescription: ".NET SDK",
			},
			Installers: []API_Installer_1_10_0{{ProductCode: tc.productCode}},
		})
	}
	return ms
}

// The version-token strip that makes SDK.8 correlate at all also makes the
// normalized-pair inclusion match every SDK major, so a correlation search
// returns several packages. The client (CompositeSource.cpp:
// GetMatchingPackage -> FindOnlyStrongMatchFieldResult) keeps such a response
// only when exactly one package matched on a strong identifier, and on a REST
// source it cannot even see which one did (MatchCriteriaResolver.cpp:
// GetBestMatchType reads Query/Filters only, never Inclusions), so a
// multi-package correlation response is discarded outright and the installed
// app vanishes from `winget list`. The server therefore has to hand back the one
// package the strong anchor identifies.
func TestCorrelationCollapsesSdkSiblingsOntoStrongAnchor(t *testing.T) {
	ms := sdkFamilyStore(t)

	// First check the premise: without a strong anchor the name pair alone fans
	// out over every SDK major.
	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "microsoftnetsdk"}},
		},
		nil,
	)
	if got := len(res); got != 3 {
		t.Fatalf("name pair alone matched %d packages, want 3 (fixture premise)", got)
	}

	// Exact wire body for an installed SDK 8.0.425: its own wix ProductCode plus
	// the two normalized-pair variants.
	res, _ = ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{ab39b1d0-4847-4c0d-a1c1-b2be697f2b76}"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "microsoftnetsdk"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "microsoftnetsdk(x64)"}},
		},
		nil,
	)
	if got := len(res); got != 1 {
		t.Fatalf("SDK correlation returned %d packages, want the 1 strong-anchored one: %v", got, res)
	}
	// Collapsing must not cost versions: 8.0.428 is what makes Available show up.
	if got := len(res["Microsoft.DotNet.SDK.8"]); got != 2 {
		t.Errorf("collapsed SDK.8 returned %d versions, want both 2", got)
	}
}

// firefoxFamilyStore reproduces the other scale effect: the community repo keeps
// one package per locale/channel variant, and because normalizePackageName drops
// bracket-enclosed groups (as the client does) "Mozilla Firefox", "(hu)",
// "(de)" and "(MSIX)" all collapse to "mozillafirefox". None of them declares
// the version-qualified wix ProductCode the installed app reports, so there is
// no strong anchor to disambiguate by.
func firefoxFamilyStore(t *testing.T) *ManifestsStore {
	t.Helper()
	ms := &ManifestsStore{}
	ms.internal = make(map[string]map[string]API_ManifestVersionInterface)

	for _, tc := range []struct{ id, version, name string }{
		{"Mozilla.Firefox", "159.0.1", "Mozilla Firefox (en-US)"},
		{"Mozilla.Firefox.hu", "159.0", "Mozilla Firefox (hu)"},
		{"Mozilla.Firefox.de", "160.0", "Mozilla Firefox (de)"},
		{"Mozilla.Firefox.MSIX", "159.0", "Mozilla Firefox (MSIX)"},
	} {
		ms.Set(tc.id, tc.version, &API_ManifestVersion_1_10_0{
			PackageVersion: tc.version,
			DefaultLocale: API_DefaultLocale_1_10_0{
				PackageName:      tc.name,
				Publisher:        "Mozilla",
				ShortDescription: "web browser",
			},
			// Real Firefox manifests declare the bare string as ProductCode,
			// which can never equal the wix keyword the client sends.
			Installers: []API_Installer_1_10_0{{ProductCode: "Mozilla Firefox"}},
		})
	}
	return ms
}

// Firefox's manifests declare no ProductCode matching the ARP keyword (a bare
// "Mozilla Firefox" string, not the version-qualified wix key), so the locale
// fan-out has no strong anchor. The live 2026-11-12 log shows the client walking
// ~100 Mozilla.Firefox.* siblings and logging "Appropriate available package
// could not be determined" - a multi-package response without a single strong
// match is discarded and Firefox vanishes from `winget list`. The ARP keyword's
// bracketed arch/locale tokens ("(x64 en-us)") still identify the installed
// variant against the siblings' own default-locale names ("(en-US)", "(hu)"),
// so exactly one candidate wins - matching how the official winget source
// correlates the install to the base package.
func TestCorrelationResolvesLocaleFanOutViaArchLocaleToken(t *testing.T) {
	ms := firefoxFamilyStore(t)

	// Exact wire body from the live log for the installed Firefox 159.0.1 (x64 en-US).
	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "mozilla firefox 159.0.1 (x64 en-us)"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "mozillafirefox"}},
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "mozillafirefox(x64)"}},
		},
		nil,
	)
	if len(res) != 1 {
		t.Fatalf("Firefox correlation returned %d packages, want the single locale-token match: %v", len(res), res)
	}
	if _, ok := res["Mozilla.Firefox"]; !ok {
		t.Errorf("locale token picked the wrong package: %v", res)
	}
}

// When the request carries no arch/locale token to intersect, the server must
// NOT guess a winner: silently picking one of the variants would serve upgrade
// metadata for a package the user did not install.
func TestCorrelationWithoutStrongAnchorOrTokenDoesNotGuess(t *testing.T) {
	ms := firefoxFamilyStore(t)

	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "NormalizedPackageNameAndPublisher", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "mozillafirefox"}},
		},
		nil,
	)
	if got := len(res); got != 4 {
		t.Errorf("tokenless Firefox fan-out returned %d packages, want all 4 left unresolved rather than guessed: %v", got, res)
	}
}

// The collapse must stay scoped to genuinely mixed-strength results. When more
// than one package is reached through a strong identifier the ambiguity is the
// client's to resolve and the server has no basis to drop either.
func TestCorrelationDoesNotCollapseMultipleStrongMatches(t *testing.T) {
	ms := sdkFamilyStore(t)

	res, _ := ms.GetByMatchFilter(
		[]API_SearchRequestPackageMatchFilter_1_1_0{
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{ab39b1d0-4847-4c0d-a1c1-b2be697f2b76}"}},
			{PackageMatchField: "ProductCode", RequestMatch: API_SearchRequestMatch_1_1_0{MatchType: "Exact", KeyWord: "{40819c02-3bd5-4e8b-9c10-101ef13de6a1}"}},
		},
		nil,
	)
	if got := len(res); got != 2 {
		t.Errorf("two strong ProductCode inclusions returned %d packages, want both 2: %v", got, res)
	}
}

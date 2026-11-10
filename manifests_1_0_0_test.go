package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rewinged/models"
)

// Manifest schema delta 1.0.0 -> 1.1.0, verified by diffing the upstream JSON
// schemas in microsoft/winget-cli:
//
//   - 1.1.0 only ADDS fields (Agreements/ReleaseNotes/ReleaseNotesUrl on locale and
//     defaultLocale; AppsAndFeaturesEntries, ElevationRequirement, ExpectedReturnCodes,
//     InstallerAbortsTerminal, InstallLocationRequired, Markets, ReleaseDate,
//     RequireExplicitUpgrade and UnsupportedOSArchitectures on installer/singleton).
//   - Nothing was removed, in properties, in the nested Installer definition, or in any
//     required list. The required sets for 1.0.0 are subsets of those for 1.1.0.
//
// Because the delta is purely additive, the Manifest_*_1_1_0 structs decode 1.0.0
// losslessly: every field present in a 1.0.0 manifest exists on the struct, and the
// fields 1.0.0 does not carry stay at their zero values.
//
// The fixtures below are copied verbatim from the real winget-pkgs community repository
// (manifests/w/WinSCP/WinSCP/5.19.3), which is what produces the reported
// "unsupported InstallerManifest version 1.0.0" errors. Note the real-world details that
// synthetic fixtures would miss: an uppercase hex InstallerSha256, two installers that
// share one URL and sha differing only by Scope/switches, a $schema comment line, and a
// non-ASCII copyright string.
const winSCPVersionManifest100 = `# yaml-language-server: $schema=https://aka.ms/winget-manifest.version.1.0.0.schema.json
PackageIdentifier: WinSCP.WinSCP
PackageVersion: 5.19.3
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.0.0
`

const winSCPInstallerManifest100 = `# yaml-language-server: $schema=https://aka.ms/winget-manifest.installer.1.0.0.schema.json
PackageIdentifier: WinSCP.WinSCP
PackageVersion: 5.19.3
MinimumOSVersion: 10.0.0.0
Installers:
- Architecture: x86
  InstallerType: inno
  Scope: machine
  InstallerUrl: https://sourceforge.net/projects/winscp/files/WinSCP/5.19.3/WinSCP-5.19.3-Setup.exe/download
  InstallerSha256: CE29F9DC7205F06FD7A82B9238C674A63F420AEDE3CEA8F86BA2BB2BB82EF86B
  InstallerSwitches:
    Custom: /NORESTART /ALLUSERS
  InstallerLocale: en-US
  UpgradeBehavior: install
- Architecture: x86
  InstallerType: inno
  Scope: user
  InstallerUrl: https://sourceforge.net/projects/winscp/files/WinSCP/5.19.3/WinSCP-5.19.3-Setup.exe/download
  InstallerSha256: CE29F9DC7205F06FD7A82B9238C674A63F420AEDE3CEA8F86BA2BB2BB82EF86B
  InstallerSwitches:
    Custom: /NORESTART /CURRENTUSER
  InstallerLocale: en-US
  UpgradeBehavior: install
ManifestType: installer
ManifestVersion: 1.0.0
`

const winSCPDefaultLocaleManifest100 = `# yaml-language-server: $schema=https://aka.ms/winget-manifest.defaultlocale.1.0.0.schema.json
PackageIdentifier: WinSCP.WinSCP
PackageVersion: 5.19.3
PackageLocale: en-US
Publisher: Martin Prikryl
PublisherSupportUrl: https://winscp.net/docs/support
PrivacyUrl: https://winscp.net/docs/privacy
Author: Martin Prikryl
PackageName: WinSCP
PackageUrl: https://winscp.net
License: GNU General Public License
LicenseUrl: https://winscp.net/eng/docs/license
Copyright: Copyright © 2000–2021 Martin Přikryl
CopyrightUrl: https://winscp.net/eng/docs/license
ShortDescription: WinSCP is an open source free SFTP client, FTP client, WebDAV client, S3 client and SCP client for Windows. Its main function is file transfer between a local and a remote computer. Beyond this, WinSCP offers scripting and basic file manager functionality.
Moniker: winscp
Tags:
- winscp
- scp
- sftp
- ftp
- ftps
- webdav
- s3
ManifestType: defaultLocale
ManifestVersion: 1.0.0
`

const winSCPSha256 = "CE29F9DC7205F06FD7A82B9238C674A63F420AEDE3CEA8F86BA2BB2BB82EF86B"

// TestManifestVersion100Ingests is the regression guard for the reported failure. Before
// 1.0.0 was added to the version dispatch, a real winget-pkgs 1.0.0 package failed all
// three files with "unsupported <Kind>Manifest version 1.0.0" and the package vanished
// from search results entirely.
func TestManifestVersion100Ingests(t *testing.T) {
	nodes := writeManifestDocs(t, winSCPVersionManifest100, winSCPInstallerManifest100, winSCPDefaultLocaleManifest100)
	if len(nodes) != 3 {
		t.Fatalf("expected 3 manifest nodes, got %d", len(nodes))
	}

	manifest, err := parseMultiFileManifest(nodes...)
	if err != nil {
		t.Fatalf("1.0.0 multi-file manifest rejected: %v", err)
	}

	mv, ok := manifest.(*models.API_Manifest_1_1_0)
	if !ok {
		t.Fatalf("API response type is %T, want *models.API_Manifest_1_1_0", manifest)
	}
	if len(mv.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(mv.Versions))
	}

	// Versions is []API_ManifestVersionInterface, so the concrete type has to be
	// asserted before the fields are reachable.
	ver, ok := mv.Versions[0].(models.API_ManifestVersion_1_1_0)
	if !ok {
		t.Fatalf("version type is %T, want models.API_ManifestVersion_1_1_0", mv.Versions[0])
	}
	if ver.PackageVersion != "5.19.3" {
		t.Errorf("PackageVersion = %q, want 5.19.3", ver.PackageVersion)
	}
	// Both installers from the real file must survive, not just the first.
	if len(ver.Installers) != 2 {
		t.Fatalf("expected 2 installers, got %d", len(ver.Installers))
	}
	// 1.0.0 has no Scope at the installer-manifest level but sets it per installer.
	scopes := map[string]bool{}
	for _, i := range ver.Installers {
		scopes[i.Scope] = true
		if i.InstallerSha256 != winSCPSha256 {
			t.Errorf("InstallerSha256 = %q, want %q", i.InstallerSha256, winSCPSha256)
		}
		if i.InstallerSwitches.Custom == "" {
			t.Error("InstallerSwitches.Custom lost during 1.0.0 parse")
		}
	}
	for _, want := range []string{"machine", "user"} {
		if !scopes[want] {
			t.Errorf("installer Scope %q missing; got scopes %v", want, scopes)
		}
	}

	body, err := json.Marshal(mv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"PackageIdentifier":"WinSCP.WinSCP"`,
		`"PackageName":"WinSCP"`,
		`"Publisher":"Martin Prikryl"`,
		// non-ASCII must survive round-tripping intact
		`Martin Přikryl`,
		`"Moniker":"winscp"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("API response missing %s\nbody: %s", want, body)
		}
	}
}

// TestNewAPIManifestAccepts100 covers the conversion branch on its own: parsing
// succeeded before this change but conversion would still have failed with
// "Converting manifest v1.0.0 data for API responses is not yet supported."
func TestNewAPIManifestAccepts100(t *testing.T) {
	nodes := writeManifestDocs(t, winSCPVersionManifest100, winSCPInstallerManifest100, winSCPDefaultLocaleManifest100)

	var (
		dl models.API_DefaultLocaleInterface
		in []models.API_InstallerInterface
	)
	for _, n := range nodes {
		switch n.ManifestType {
		case "defaultLocale":
			m, err := unmarshalDefaultLocaleManifest(n.ManifestVersion, n.Node)
			if err != nil {
				t.Fatal(err)
			}
			dl = m.ToApiDefaultLocale()
		case "installer":
			m, err := unmarshalInstallerManifest(n.ManifestVersion, n.Node)
			if err != nil {
				t.Fatal(err)
			}
			in = append(in, m.ToApiInstallers()...)
		}
	}
	if len(in) != 2 {
		t.Fatalf("expected 2 installers, got %d", len(in))
	}

	manifest, err := newAPIManifest("1.0.0", "WinSCP.WinSCP", "5.19.3", dl, nil, in)
	if err != nil {
		t.Fatalf("newAPIManifest rejected ManifestVersion 1.0.0: %v", err)
	}
	if _, ok := manifest.(*models.API_Manifest_1_1_0); !ok {
		t.Errorf("API response type is %T, want *models.API_Manifest_1_1_0", manifest)
	}
}

// TestSingletonManifestVersion100Ingests covers the singleton path. Singleton manifests
// predate multi-file manifests, so the oldest packages are disproportionately singletons.
func TestSingletonManifestVersion100Ingests(t *testing.T) {
	const singleton100 = `PackageIdentifier: Test.Singleton100
PackageVersion: "1.0.0"
PackageLocale: en-US
Publisher: Test Publisher
PackageName: Singleton100
License: MIT
ShortDescription: A singleton manifest using the 1.0.0 schema
Moniker: s100
Tags:
- test
ManifestType: singleton
ManifestVersion: 1.0.0
Installers:
  - Architecture: x64
    InstallerType: exe
    InstallerUrl: https://example.invalid/singleton.exe
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266b
`
	nodes := writeManifestDocs(t, singleton100)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 manifest node, got %d", len(nodes))
	}
	manifest, err := parseNodeAsSingletonManifest("1.0.0", nodes[0].Node)
	if err != nil {
		t.Fatalf("1.0.0 singleton manifest rejected: %v", err)
	}
	if _, ok := manifest.(*models.API_Manifest_1_1_0); !ok {
		t.Errorf("API response type is %T, want *models.API_Manifest_1_1_0", manifest)
	}
}

// TestManifestVersion100LocaleFieldsAreZero documents the one real consequence of the
// additive delta: fields introduced in 1.1.0 cannot exist in a 1.0.0 manifest, so they
// stay empty rather than being invented. Agreements/ReleaseNotes are exactly that.
func TestManifestVersion100LocaleFieldsAreZero(t *testing.T) {
	nodes := writeManifestDocs(t, winSCPVersionManifest100, winSCPInstallerManifest100, winSCPDefaultLocaleManifest100)
	manifest, err := parseMultiFileManifest(nodes...)
	if err != nil {
		t.Fatal(err)
	}
	mv := manifest.(*models.API_Manifest_1_1_0)
	ver, ok := mv.Versions[0].(models.API_ManifestVersion_1_1_0)
	if !ok {
		t.Fatalf("version type is %T, want models.API_ManifestVersion_1_1_0", mv.Versions[0])
	}
	dl := ver.DefaultLocale
	if len(dl.Agreements) != 0 {
		t.Errorf("Agreements should be empty for a 1.0.0 manifest, got %d", len(dl.Agreements))
	}
	if dl.ReleaseNotes != "" || dl.ReleaseNotesUrl != "" {
		t.Errorf("ReleaseNotes fields should be empty for a 1.0.0 manifest, got %q / %q",
			dl.ReleaseNotes, dl.ReleaseNotesUrl)
	}
	// Fields that DO exist in 1.0.0 must be populated.
	if dl.Moniker != "winscp" {
		t.Errorf("Moniker = %q, want winscp", dl.Moniker)
	}
	if len(dl.Tags) != 7 {
		t.Errorf("Tags = %v, want 7 entries", dl.Tags)
	}
}

// TestManifestVersion100UppercaseShaInternalization pins the key-casing contract that
// real 1.0.0 manifests exercise: winget-pkgs writes InstallerSha256 in UPPERCASE hex,
// while the installer store names files in lowercase and GetPackageHandler rewrites
// InstallerUrl with strings.ToLower. InstallerSources must therefore be keyed lowercase
// (that is what the download handler receives), while InternalizedInstallers is keyed
// with the raw casing because that is what packages.go reads back. If either side
// drifts, on-demand download 404s on a valid package.
func TestManifestVersion100UppercaseShaInternalization(t *testing.T) {
	nodes := writeManifestDocs(t, winSCPVersionManifest100, winSCPInstallerManifest100, winSCPDefaultLocaleManifest100)
	manifest, err := parseMultiFileManifest(nodes...)
	if err != nil {
		t.Fatal(err)
	}
	ver := manifest.(*models.API_Manifest_1_1_0).Versions[0].(models.API_ManifestVersion_1_1_0)

	var installers []models.API_InstallerInterface
	for i := range ver.Installers {
		installers = append(installers, &ver.Installers[i])
	}

	rawSha := installers[0].GetInstallerSha()
	if rawSha != winSCPSha256 {
		t.Fatalf("expected the manifest's uppercase sha %q, got %q", winSCPSha256, rawSha)
	}
	lowerSha := strings.ToLower(rawSha)

	dir := t.TempDir()
	internalizeInstallers("WinSCP.WinSCP", "5.19.3", installers, true, dir, nil)

	if _, ok := models.InstallerSources.Get(lowerSha); !ok {
		t.Errorf("InstallerSources has no entry for lowercase sha %q; the request path would not match", lowerSha)
	}
	if _, ok := models.InstallerSources.Get(rawSha); ok {
		t.Error("InstallerSources must not be keyed by the raw uppercase sha")
	}
	if !models.InternalizedInstallers.Get(rawSha) {
		t.Error("installer not marked internalized under the casing GetPackageHandler reads")
	}
	if got := storedFiles(t, dir); len(got) != 0 {
		t.Errorf("on-demand mode must not write files at ingest, found %v", got)
	}
}

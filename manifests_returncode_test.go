package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rewinged/models"

	"gopkg.in/yaml.v3"
)

// Regression guard for the ingest failures reported as:
//
//	cannot unmarshal installer manifest error="yaml: unmarshal errors:
//	  line 14: cannot unmarshal !!str `3010` into int64 ..."
//	could not parse all manifest files for this package
//	  error="no (valid) installer manifest" package=SonicPi.SonicPi
//
// The winget manifest schema declares definitions/InstallerReturnCode as an
// integer (and it is the ONLY integer-typed definition across every schema
// version and manifest kind), but real manifests in microsoft/winget-pkgs quote
// the value anyway, which a plain int64 field cannot decode. The fixtures below
// are verbatim from the packages in the reported error log:
// SonicPi.SonicPi 3.3.1 (schema 1.1.0) and ShareFile.ShareFileForWindows
// 24.3.3.0 (schema 1.5.0).

const sonicPiVersionManifest = `# Created with YamlCreate.ps1 v2.0.6 $debug=NVS1.7-2-1
# yaml-language-server: $schema=https://aka.ms/winget-manifest.version.1.1.0.schema.json

PackageIdentifier: SonicPi.SonicPi
PackageVersion: 3.3.1
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.1.0
`

const sonicPiInstallerManifest = `# Created with YamlCreate.ps1 v2.0.6 $debug=NVS1.7-2-1
# yaml-language-server: $schema=https://aka.ms/winget-manifest.installer.1.1.0.schema.json

PackageIdentifier: SonicPi.SonicPi
PackageVersion: 3.3.1
InstallerLocale: en-US
MinimumOSVersion: 10.0.0.0
InstallerType: wix
Scope: machine
InstallModes:
- interactive
- silent
- silentWithProgress
InstallerSuccessCodes:
- "3010"
UpgradeBehavior: install
FileExtensions:
- rb
- txt
ReleaseDate: 2021-02-01
Installers:
- Architecture: x64
  InstallerUrl: https://sonic-pi.net/files/releases/v3.3.1/Sonic-Pi-for-Win-x64-v3.3.1.msi
  InstallerSha256: B9EE2605333B01FEB6CE973E5918F04F489DE83B971A46890E1313EAE7C04B79
  ProductCode: '{5E6D24D8-32B5-4516-B266-A7C20D3347E3}'
ManifestType: installer
ManifestVersion: 1.1.0
`

const sonicPiDefaultLocaleManifest = `# Created with YamlCreate.ps1 v2.0.6 $debug=NVS1.7-2-1
# yaml-language-server: $schema=https://aka.ms/winget-manifest.defaultLocale.1.1.0.schema.json

PackageIdentifier: SonicPi.SonicPi
PackageVersion: 3.3.1
PackageLocale: en-US
Publisher: Sonic Pi
# PublisherUrl: 
PublisherSupportUrl: https://github.com/sonic-pi-net/sonic-pi/issues
# PrivacyUrl: 
Author: Sam Aaron
PackageName: Sonic Pi
PackageUrl: https://sonic-pi.net/
License: MIT
LicenseUrl: https://raw.githubusercontent.com/sonic-pi-net/sonic-pi/dev/LICENSE.md
Copyright: Copyright (c) Samuel Aaron and contributors (sam@sonic-pi.net)
# CopyrightUrl: 
ShortDescription: Sonic Pi is a code-based music creation and performance tool.
# Description: 
Moniker: sonic-pi
Tags:
- audio
- live-coding
- music
- ruby
- synth
# Agreements: 
# ReleaseNotes: 
# ReleaseNotesUrl: 
ManifestType: defaultLocale
ManifestVersion: 1.1.0
`

const shareFileVersionManifest = `PackageIdentifier: ShareFile.ShareFileForWindows
PackageVersion: 24.3.3.0
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.5.0
`

const shareFileInstallerManifest = `# Created with YamlCreate.ps1 v2.2.13 $debug=MDSU.LF.7-4-1.Unix
# yaml-language-server: $schema=https://aka.ms/winget-manifest.installer.1.5.0.schema.json

PackageIdentifier: ShareFile.ShareFileForWindows
PackageVersion: 24.3.3.0
InstallerLocale: en-US
InstallerType: msi
Scope: machine
InstallModes:
- interactive
- silent
- silentWithProgress
InstallerSuccessCodes:
- "3010"
- "1641"
UpgradeBehavior: install
Installers:
- Architecture: x64
  InstallerUrl: https://dl.sharefile.com/cfwin/ShareFileForWindows_x64_v24.3.3.0.msi
  InstallerSha256: 62C00292AD42C3C94098D4030C78B190C80421A58B180F6851BF5D45D7B67C85
  ProductCode: '{CF8E6E00-9C03-4440-81C0-21FACB921A6B}'
ManifestType: installer
ManifestVersion: 1.5.0
`

const shareFileDefaultLocaleManifest = `# Created with YamlCreate.ps1 v2.2.13 $debug=MDSU.LF.7-4-1.Unix
# yaml-language-server: $schema=https://aka.ms/winget-manifest.defaultLocale.1.5.0.schema.json

PackageIdentifier: ShareFile.ShareFileForWindows
PackageVersion: 24.3.3.0
PackageLocale: en-US
Publisher: ShareFile
PublisherUrl: https://sharefile.com
PublisherSupportUrl: https://support.sharefile.com/s/
# PrivacyUrl: 
# Author: 
PackageName: ShareFile For Windows
PackageUrl: https://www.sharefile.com/apps-integrations
License: Proprietary
# LicenseUrl: 
# Copyright: 
# CopyrightUrl: 
ShortDescription: ShareFile for Windows allows you to access your files directly through a mapped drive. ShareFile provides a native Windows Explorer experience.
# Description: 
# Moniker: 
Tags:
- citrixfiles
- sharefile
# ReleaseNotes: 
ReleaseNotesUrl: https://docs.sharefile.com/en-us/sharefile/whats-new
# PurchaseUrl: 
# InstallationNotes: 
# Documentations: 
ManifestType: defaultLocale
ManifestVersion: 1.5.0
`

// TestQuotedInstallerSuccessCodesIngest is the end-to-end regression guard for
// both reported packages: the full three-file package must ingest and be served.
// It also pins the serving contract: the REST API specs declare these fields as
// integers, so quoted values from disk must still come out as JSON numbers.
func TestQuotedInstallerSuccessCodesIngest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		docs       []string
		wantInJSON []string
	}{
		{
			name: "SonicPi.SonicPi 3.3.1, schema 1.1.0",
			docs: []string{sonicPiVersionManifest, sonicPiInstallerManifest, sonicPiDefaultLocaleManifest},
			wantInJSON: []string{
				`"PackageIdentifier":"SonicPi.SonicPi"`,
				`"InstallerSuccessCodes":[3010]`, // quoted on disk, numeric on the wire
				`"InstallerSha256":"B9EE2605333B01FEB6CE973E5918F04F489DE83B971A46890E1313EAE7C04B79"`,
			},
		},
		{
			name: "ShareFile.ShareFileForWindows 24.3.3.0, schema 1.5.0",
			docs: []string{shareFileVersionManifest, shareFileInstallerManifest, shareFileDefaultLocaleManifest},
			wantInJSON: []string{
				`"PackageIdentifier":"ShareFile.ShareFileForWindows"`,
				`"InstallerSuccessCodes":[3010,1641]`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := writeManifestDocs(t, tc.docs...)
			manifest, err := parseMultiFileManifest(nodes...)
			if err != nil {
				t.Fatalf("package rejected at ingest: %v", err)
			}
			body, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantInJSON {
				if !strings.Contains(string(body), want) {
					t.Errorf("API response missing %s\nbody: %s", want, body)
				}
			}
		})
	}
}

// TestQuotedReturnCodesTeethCheck proves the failure mode being fixed really
// comes from yaml.v3 refusing to coerce a quoted scalar into a number, and that
// models.InstallerReturnCode (not a global loosening of decoding) is what fixes
// it. The control decodes the same document into a plain int64 slice and must
// still fail with the exact reported error.
func TestQuotedReturnCodesTeethCheck(t *testing.T) {
	var control struct {
		InstallerSuccessCodes []int64 `yaml:"InstallerSuccessCodes"`
	}
	err := yaml.Unmarshal([]byte(sonicPiInstallerManifest), &control)
	if err == nil {
		t.Fatal("expected yaml.v3 to reject a quoted code into int64, got nil — the premise of the fix is wrong")
	}
	if !strings.Contains(err.Error(), "cannot unmarshal !!str `3010` into int64") {
		t.Errorf("control error = %q, want it to contain the reported unmarshal error", err)
	}
}

// TestQuotedReturnCodesAllFieldPositions covers the two other positions the
// integer definition is reached from, which the upstream fixtures above do not:
// per-installer InstallerSuccessCodes and quoted ExpectedReturnCodes[].
// InstallerReturnCode. Values must also survive manifest->API conversion.
func TestQuotedReturnCodesAllFieldPositions(t *testing.T) {
	const doc = `PackageIdentifier: Test.ReturnCodes
PackageVersion: "1.0.0"
InstallerLocale: en-US
MinimumOSVersion: 10.0.0.0
InstallerType: msi
InstallerSuccessCodes:
  - "3010"
ExpectedReturnCodes:
  - InstallerReturnCode: "1603"
    ReturnResponse: installBlocked
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/x64.msi
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266
    InstallerSuccessCodes:
      - "1641"
    ExpectedReturnCodes:
      - InstallerReturnCode: "-1073741819"
        ReturnResponse: installerError
ManifestType: installer
ManifestVersion: 1.10.0
`
	nodes := writeManifestDocs(t, doc)
	installer, err := unmarshalInstallerManifest("1.10.0", nodes[0].Node)
	if err != nil {
		t.Fatalf("quoted return codes rejected: %v", err)
	}

	manifestLevel, ok := installer.(*models.Manifest_InstallerManifest_1_10_0)
	if !ok {
		t.Fatalf("parsed type is %T, want *models.Manifest_InstallerManifest_1_10_0", installer)
	}
	if got := manifestLevel.InstallerSuccessCodes; len(got) != 1 || got[0] != 3010 {
		t.Errorf("manifest-level InstallerSuccessCodes = %v, want [3010]", got)
	}
	if got := manifestLevel.ExpectedReturnCodes; len(got) != 1 || got[0].InstallerReturnCode != 1603 {
		t.Errorf("manifest-level ExpectedReturnCodes = %v, want one entry with code 1603", got)
	}

	perInstaller := manifestLevel.Installers
	if len(perInstaller) != 1 {
		t.Fatalf("expected 1 installer, got %d", len(perInstaller))
	}
	if got := perInstaller[0].InstallerSuccessCodes; len(got) != 1 || got[0] != 1641 {
		t.Errorf("per-installer InstallerSuccessCodes = %v, want [1641]", got)
	}
	if got := perInstaller[0].ExpectedReturnCodes; len(got) != 1 || got[0].InstallerReturnCode != -1073741819 {
		t.Errorf("per-installer ExpectedReturnCodes = %v, want one entry with code -1073741819", got)
	}

	// manifest -> API conversion must carry the values through and serve them
	// as JSON numbers. Note ToApiInstaller merges the manifest-level codes into
	// each installer with nonDefault(), so where a per-installer value exists it
	// wins; the manifest-level 1603 is asserted above, on the parsed struct.
	apiInstallers := installer.ToApiInstallers()
	body, err := json.Marshal(apiInstallers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"InstallerSuccessCodes":[1641]`,
		`"InstallerReturnCode":-1073741819`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("API conversion missing %s\nbody: %s", want, body)
		}
	}
}

// TestReturnCodeUnquotedSpellingsParity pins the subtle risk in accepting quoted
// codes: the custom decoder must not reject or reinterpret any spelling that a
// plain int64 field already accepted. Each unquoted spelling is decoded twice,
// once into plain int64 and once into models.InstallerReturnCode, and the two
// results must agree. yaml.v3 resolves 0x0BBA to 3002, leading-zero 010 to
// octal 8 and 3_010 to 3010, so a hand-rolled base-10 parse would have silently
// broken all three.
func TestReturnCodeUnquotedSpellingsParity(t *testing.T) {
	spellings := []string{
		"3010",
		"+3010",
		"-1073741819",
		"0x0BBA",
		"0xC0000005",
		"0o10",
		"010",
		"0b101",
		"3_010",
		"3010.0",
		"~",
		"null",
	}

	for _, spelling := range spellings {
		t.Run(spelling, func(t *testing.T) {
			doc := "codes:\n  - " + spelling + "\n"

			var plain struct {
				Codes []int64 `yaml:"codes"`
			}
			plainErr := yaml.Unmarshal([]byte(doc), &plain)

			var lenient struct {
				Codes []models.InstallerReturnCode `yaml:"codes"`
			}
			lenientErr := yaml.Unmarshal([]byte(doc), &lenient)

			if (plainErr == nil) != (lenientErr == nil) {
				t.Fatalf("acceptance differs: plain int64 err=%v, InstallerReturnCode err=%v", plainErr, lenientErr)
			}
			if plainErr != nil {
				return // both rejected: parity holds
			}
			// A null sequence member is dropped by yaml.v3 for both types, so the
			// lengths are compared rather than assumed to be one.
			if len(plain.Codes) != len(lenient.Codes) {
				t.Fatalf("lengths differ: plain int64 = %v, InstallerReturnCode = %v", plain.Codes, lenient.Codes)
			}
			for i := range plain.Codes {
				if int64(lenient.Codes[i]) != plain.Codes[i] {
					t.Errorf("%s: InstallerReturnCode[%d] = %d, plain int64 = %d", spelling, i, lenient.Codes[i], plain.Codes[i])
				}
			}
		})
	}

	// Quoted spellings are read as the decimal the author wrote, so "010" is ten
	// rather than octal eight.
	var quoted struct {
		Codes []models.InstallerReturnCode `yaml:"codes"`
	}
	if err := yaml.Unmarshal([]byte("codes:\n  - \"010\"\n  - \"-1603\"\n"), &quoted); err != nil {
		t.Fatalf("quoted codes rejected: %v", err)
	}
	if len(quoted.Codes) != 2 || quoted.Codes[0] != 10 || quoted.Codes[1] != -1603 {
		t.Errorf("quoted codes = %v, want [10 -1603]", quoted.Codes)
	}
}

// TestReturnCodeUnquotedStillWorks guards the other direction: plain integers
// (the schema-correct spelling) must keep parsing, and non-numeric garbage must
// still be rejected rather than swallowed by the custom decoder.
func TestReturnCodeUnquotedStillWorks(t *testing.T) {
	const unquoted = `PackageIdentifier: Test.Codes
PackageVersion: "1.0.0"
InstallerLocale: en-US
MinimumOSVersion: 10.0.0.0
InstallerType: exe
InstallerSuccessCodes:
  - 127
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/x64.exe
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266
ManifestType: installer
ManifestVersion: 1.10.0
`
	nodes := writeManifestDocs(t, unquoted)
	installer, err := unmarshalInstallerManifest("1.10.0", nodes[0].Node)
	if err != nil {
		t.Fatalf("unquoted return code rejected: %v", err)
	}
	manifestLevel, ok := installer.(*models.Manifest_InstallerManifest_1_10_0)
	if !ok {
		t.Fatalf("parsed type is %T, want *models.Manifest_InstallerManifest_1_10_0", installer)
	}
	if got := manifestLevel.InstallerSuccessCodes; len(got) != 1 || got[0] != 127 {
		t.Errorf("InstallerSuccessCodes = %v, want [127]", got)
	}

	const garbage = `PackageIdentifier: Test.BadCodes
PackageVersion: "1.0.0"
InstallerLocale: en-US
MinimumOSVersion: 10.0.0.0
InstallerType: exe
InstallerSuccessCodes:
  - "not-a-number"
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/x64.exe
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266
ManifestType: installer
ManifestVersion: 1.10.0
`
	nodes = writeManifestDocs(t, garbage)
	if _, err := unmarshalInstallerManifest("1.10.0", nodes[0].Node); err == nil {
		t.Fatal("expected a non-numeric return code to be rejected, got nil")
	} else if !strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("error = %q, want it to name the offending value", err)
	}
}

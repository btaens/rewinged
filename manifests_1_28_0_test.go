package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rewinged/models"

	"gopkg.in/yaml.v3"
)

// Manifest schema delta 1.12.0 -> 1.28.0, verified by diffing the upstream JSON
// schemas in microsoft/winget-cli:
//
//   - version / locale / defaultLocale schemas are byte-identical except for the
//     version string embedded in $id, description and the ManifestVersion default.
//   - installer and singleton gain exactly one optional field,
//     DesiredStateConfiguration (DSC resource references), at both the manifest
//     level and on each installer entry.
//   - nothing was removed, no enum changed, and no field became required.
//
// The fixtures below are written against the 1.28.0 required-field lists, so they
// also fail if the fixtures themselves drift from the schema.
const sha128 = "6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266b"

const versionManifest128 = `PackageIdentifier: Test.OneTwentyEight
PackageVersion: "1.0.0"
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.28.0
`

const installerManifest128 = `PackageIdentifier: Test.OneTwentyEight
PackageVersion: "1.0.0"
InstallerLocale: en-US
Platform:
  - Windows.Desktop
MinimumOSVersion: "10.0.0.0"
InstallerType: msi
Scope: machine
InstallModes:
  - silent
UpgradeBehavior: install
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/test-x64.msi
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266b
    InstallerLocale: en-US
    DesiredStateConfiguration:
      PowerShell:
        - RepositoryUrl: https://www.powershellgallery.com/packages/TestModule
          ModuleName: TestModule
          Resources:
            - Name: TestResource
      DSCv3:
        Resources:
          - Type: Microsoft.Test/Resource
DesiredStateConfiguration:
  DSCv3:
    Resources:
      - Type: Microsoft.Test/PackageResource
ManifestType: installer
ManifestVersion: 1.28.0
`

const defaultLocaleManifest128 = `PackageIdentifier: Test.OneTwentyEight
PackageVersion: "1.0.0"
PackageLocale: en-US
Publisher: Test Publisher
PackageName: OneTwentyEight
License: MIT
ShortDescription: A package using manifest version 1.28.0
ManifestType: defaultLocale
ManifestVersion: 1.28.0
`

const singletonManifest128 = `PackageIdentifier: Test.Singleton128
PackageVersion: "1.0.0"
PackageLocale: en-US
Publisher: Test Publisher
PackageName: Singleton128
License: MIT
ShortDescription: A singleton manifest using 1.28.0
ManifestType: singleton
ManifestVersion: 1.28.0
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/singleton.exe
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266b
DesiredStateConfiguration:
  DSCv3:
    Resources:
      - Type: Microsoft.Test/Resource
`

// writeManifestDocs writes each YAML document to its own file and parses it the way
// ingestManifests does, so the tests exercise the real file->node->manifest path.
func writeManifestDocs(t *testing.T, docs ...string) []models.ManifestNode {
	t.Helper()
	dir := t.TempDir()
	var nodes []models.ManifestNode
	for i, doc := range docs {
		path := filepath.Join(dir, fmt.Sprintf("manifest-%d.yaml", i))
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		parsed, err := parseFileAsBaseManifests(path)
		if err != nil {
			t.Fatalf("parseFileAsBaseManifests(%s): %v", path, err)
		}
		for _, p := range parsed {
			nodes = append(nodes, *p)
		}
	}
	return nodes
}

// TestManifestVersion128Ingests is the regression guard. Before 1.28.0 was added to
// the version dispatch, ingesting these manifests failed with
// "unsupported InstallerManifest version 1.28.0".
func TestManifestVersion128Ingests(t *testing.T) {
	nodes := writeManifestDocs(t, versionManifest128, installerManifest128, defaultLocaleManifest128)
	if len(nodes) != 3 {
		t.Fatalf("expected 3 manifest nodes, got %d", len(nodes))
	}

	manifest, err := parseMultiFileManifest(nodes...)
	if err != nil {
		t.Fatalf("1.28.0 multi-file manifest rejected: %v", err)
	}

	// The response uses the API 1.10.0 shape: upstream publishes no newer REST API
	// schema, and the manifest version never appears in an API response body.
	mv, ok := manifest.(*models.API_Manifest_1_10_0)
	if !ok {
		t.Fatalf("API response type is %T, want *models.API_Manifest_1_10_0", manifest)
	}
	if len(mv.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(mv.Versions))
	}

	body, err := json.Marshal(mv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"PackageIdentifier":"Test.OneTwentyEight"`,
		`"InstallerSha256":"` + sha128 + `"`,
		`"InstallerUrl":"https://example.invalid/test-x64.msi"`,
		`"PackageName":"OneTwentyEight"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("API response missing %s\nbody: %s", want, body)
		}
	}
}

// TestManifestVersion128DSCNotServed pins the deliberate limitation: no published
// winget REST API schema (newest is 1.10.0) has a field for DSC data, so rewinged
// tolerates DesiredStateConfiguration in manifests but cannot serve it. If upstream
// publishes an API schema that carries DSC, this test marks where that decision is
// revisited instead of silently forgotten.
func TestManifestVersion128DSCNotServed(t *testing.T) {
	nodes := writeManifestDocs(t, versionManifest128, installerManifest128, defaultLocaleManifest128)
	manifest, err := parseMultiFileManifest(nodes...)
	if err != nil {
		t.Fatalf("1.28.0 multi-file manifest rejected: %v", err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "desiredstateconfiguration") {
		t.Error("DSC data leaked into an API response that has no field for it")
	}
}

// TestSingletonManifestVersion128Ingests covers the singleton path, which also
// gained DesiredStateConfiguration in 1.28.0.
func TestSingletonManifestVersion128Ingests(t *testing.T) {
	nodes := writeManifestDocs(t, singletonManifest128)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 manifest node, got %d", len(nodes))
	}
	manifest, err := parseNodeAsSingletonManifest("1.28.0", nodes[0].Node)
	if err != nil {
		t.Fatalf("1.28.0 singleton manifest rejected: %v", err)
	}
	if _, ok := manifest.(*models.API_Manifest_1_10_0); !ok {
		t.Errorf("API response type is %T, want *models.API_Manifest_1_10_0", manifest)
	}
}

// TestUnknownManifestVersionStillRejected guards the other side of the change:
// widening the dispatch must not turn the version gate into an accept-anything
// gate, or a future schema would be silently parsed as the 1.10.0 shape.
//
// This asserts on the unmarshal functions directly, because parseMultiFileManifest
// deliberately logs a bad manifest and continues, surfacing only an aggregated
// "no (valid) installer manifest". The precise per-version rejection lives here.
func TestUnknownManifestVersionStillRejected(t *testing.T) {
	const futureInstaller = `PackageIdentifier: Test.Future
PackageVersion: "1.0.0"
InstallerLocale: en-US
Platform:
  - Windows.Desktop
MinimumOSVersion: "10.0.0.0"
InstallerType: msi
ManifestType: installer
ManifestVersion: 1.99.0
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/x.msi
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266b
`
	const futureSingleton = `PackageIdentifier: Test.Future
PackageVersion: "1.0.0"
PackageLocale: en-US
Publisher: Test
PackageName: Future
License: MIT
ShortDescription: future
ManifestType: singleton
ManifestVersion: 1.99.0
Installers:
  - Architecture: x64
    InstallerUrl: https://example.invalid/x.exe
    InstallerSha256: 6ba7b8109dad11d180b400a0c9d2266b6ba7b8109dad11d180b400a0c9d2266b
`
	const futureVersion = `PackageIdentifier: Test.Future
PackageVersion: "1.0.0"
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.99.0
`
	const futureLocale = `PackageIdentifier: Test.Future
PackageVersion: "1.0.0"
PackageLocale: en-US
Publisher: Test
PackageName: Future
License: MIT
ShortDescription: future
ManifestType: locale
ManifestVersion: 1.99.0
`
	const futureDefaultLocale = `PackageIdentifier: Test.Future
PackageVersion: "1.0.0"
PackageLocale: en-US
Publisher: Test
PackageName: Future
License: MIT
ShortDescription: future
ManifestType: defaultLocale
ManifestVersion: 1.99.0
`

	cases := []struct {
		name      string
		doc       string
		unmarshal func(string, yaml.Node) error
		wantErr   string
	}{
		{
			name: "installer manifest",
			doc:  futureInstaller,
			unmarshal: func(v string, n yaml.Node) error {
				_, err := unmarshalInstallerManifest(v, n)
				return err
			},
			wantErr: "unsupported InstallerManifest version 1.99.0",
		},
		{
			name: "singleton manifest",
			doc:  futureSingleton,
			unmarshal: func(v string, n yaml.Node) error {
				_, err := unmarshalSingletonManifest(v, n)
				return err
			},
			wantErr: "unsupported SingletonManifest version 1.99.0",
		},
		{
			name: "version manifest",
			doc:  futureVersion,
			unmarshal: func(v string, n yaml.Node) error {
				_, err := unmarshalVersionManifest(v, n)
				return err
			},
			wantErr: "unsupported VersionManifest version 1.99.0",
		},
		{
			name: "locale manifest",
			doc:  futureLocale,
			unmarshal: func(v string, n yaml.Node) error {
				_, err := unmarshalLocaleManifest(v, n)
				return err
			},
			wantErr: "unsupported LocaleManifest version 1.99.0",
		},
		{
			name: "defaultLocale manifest",
			doc:  futureDefaultLocale,
			unmarshal: func(v string, n yaml.Node) error {
				_, err := unmarshalDefaultLocaleManifest(v, n)
				return err
			},
			wantErr: "unsupported DefaultLocaleManifest version 1.99.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := writeManifestDocs(t, tc.doc)
			err := tc.unmarshal("1.99.0", nodes[0].Node)
			if err == nil {
				t.Fatal("expected an error for an unsupported manifest version, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestNewAPIManifestAccepts128 covers the API-conversion branch directly: a 1.28.0
// manifest that parses must also convert, otherwise ingestion fails with
// "Converting manifest v1.28.0 data for API responses is not yet supported."
func TestNewAPIManifestAccepts128(t *testing.T) {
	nodes := writeManifestDocs(t, versionManifest128, installerManifest128, defaultLocaleManifest128)

	var (
		dl models.API_DefaultLocaleInterface
		l  []models.API_LocaleInterface
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
	if len(in) != 1 {
		t.Fatalf("expected 1 installer, got %d", len(in))
	}

	manifest, err := newAPIManifest("1.28.0", "Test.OneTwentyEight", "1.0.0", dl, l, in)
	if err != nil {
		t.Fatalf("newAPIManifest rejected ManifestVersion 1.28.0: %v", err)
	}
	if _, ok := manifest.(*models.API_Manifest_1_10_0); !ok {
		t.Errorf("API response type is %T, want *models.API_Manifest_1_10_0", manifest)
	}
}

package models

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"rewinged/logging"

	"gopkg.in/yaml.v3"
)

// This holds all BaseManifest information for easy access as well as
// a complete copy of the YAML document or node as ingested from disk.
type ManifestNode struct {
	BaseManifest `yaml:",inline"`
	Node         yaml.Node `yaml:"-"`
}

func getMapValues[M ~map[K]V, K comparable, V any](m M) []V {
	r := make([]V, 0, len(m))
	for _, v := range m {
		r = append(r, v)
	}
	return r
}

func caseInsensitiveContains(s, substr string) bool {
	s, substr = strings.ToUpper(s), strings.ToUpper(substr)
	return strings.Contains(s, substr)
}

// Modified from: https://stackoverflow.com/a/38407429
func findField(v interface{}, name string) reflect.Value {
	// create queue of values to search. Start with the function arg.
	queue := []reflect.Value{reflect.ValueOf(v)}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		// dereference pointers
		for v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		// check all elements in slices
		if v.Kind() == reflect.Slice {
			if v.Len() > 0 {
				for i := 0; i < v.Len(); i++ {
					queue = append(queue, v.Index(i))
				}
			} else {
				//fmt.Println("CONTINUE (EMPTY SLICE)")
				continue
			}
		}
		// ignore if this is not a struct
		if v.Kind() != reflect.Struct {
			//fmt.Println("CONTINUE (NOT STRUCT)")
			continue
		}
		// iterate through fields looking for match on name
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			//fmt.Println("TESTING FIELD", t.Field(i).Name)
			if t.Field(i).Name == name {
				// found it!
				//fmt.Println("FOUND THE FIELD", name, "!")
				return v.Field(i)
			}
			// push field to queue
			queue = append(queue, v.Field(i))
		}
	}
	return reflect.Value{}
}

// Internal in-memory list of successfully internalized installers
type InternalizedInstallersStore struct {
	sync.RWMutex
	internal map[string]bool
}

func (store *InternalizedInstallersStore) Set(installerSha string, isInternalized bool) {
	store.Lock()
	store.internal[installerSha] = isInternalized
	store.Unlock()
}

func (store *InternalizedInstallersStore) Get(installerSha string) bool {
	store.RLock()
	defer store.RUnlock()
	return store.internal[installerSha]
}

// Internal in-memory mapping of installer SHA (lowercase) to its original
// InstallerUrl. Populated during manifest ingestion when on-demand
// internalization is enabled but the installer has not been downloaded yet,
// so the /installers/ handler knows where to fetch it from on first request.
type InstallerSourcesStore struct {
	sync.RWMutex
	internal map[string]string
}

func (store *InstallerSourcesStore) Set(installerSha string, installerUrl string) {
	store.Lock()
	store.internal[installerSha] = installerUrl
	store.Unlock()
}

func (store *InstallerSourcesStore) Get(installerSha string) (string, bool) {
	store.RLock()
	defer store.RUnlock()
	url, ok := store.internal[installerSha]
	return url, ok
}

// Internal in-memory data store of all manifest data
type ManifestsStore struct {
	sync.RWMutex
	internal map[string]map[string]API_ManifestVersionInterface
}

func (ms *ManifestsStore) Set(packageidentifier string, packageversion string, value API_ManifestVersionInterface) {
	ms.Lock()
	vmap, ok := ms.internal[packageidentifier]
	if !ok {
		vmap = make(map[string]API_ManifestVersionInterface)
		ms.internal[packageidentifier] = vmap
	}
	vmap[packageversion] = value
	ms.Unlock()
}

func (ms *ManifestsStore) GetAllVersions(packageidentifier string) (value []API_ManifestVersionInterface) {
	ms.RLock()
	result := getMapValues(ms.internal[packageidentifier])
	ms.RUnlock()
	return result
}

func (ms *ManifestsStore) Get(packageidentifier string, packageversion string) (value API_ManifestVersionInterface) {
	ms.RLock()
	result := ms.internal[packageidentifier][packageversion]
	ms.RUnlock()
	return result
}

func (ms *ManifestsStore) GetAll() (value map[string][]API_ManifestVersionInterface) {
	ms.RLock()
	var m = make(map[string][]API_ManifestVersionInterface)
	for k := range ms.internal {
		m[k] = getMapValues(ms.internal[k])
	}
	ms.RUnlock()
	return m
}

func (ms *ManifestsStore) GetAllPackageIdentifiers() (value []API_Package) {
	ms.RLock()
	var p []API_Package
	for k := range ms.internal {
		p = append(p, API_Package{
			PackageIdentifier: k,
		})
	}
	ms.RUnlock()
	return p
}

func (ms *ManifestsStore) GetManifestCount() (value int) {
	ms.RLock()
	count := len(ms.internal)
	ms.RUnlock()
	return count
}

func (ms *ManifestsStore) GetByKeyword(keyword string) map[string][]API_ManifestVersionInterface {
	var manifestResultsMap = make(map[string][]API_ManifestVersionInterface)
	ms.RLock()
	for packageIdentifier, packageVersions := range ms.internal {
		for _, version := range packageVersions {
			if caseInsensitiveContains(version.GetDefaultLocalePackageName(), keyword) || caseInsensitiveContains(version.GetDefaultLocaleShortDescription(), keyword) {
				manifestResultsMap[packageIdentifier] = append(manifestResultsMap[packageIdentifier], version)
			}
		}
	}
	ms.RUnlock()

	return manifestResultsMap
}

// nonEmpty returns the values that are not the empty string, preserving order.
// The API version structs use it to filter the correlation identifiers
// (ProductCode, PackageFamilyName, UpgradeCode, ...) they collect from
// installer fields that are optional in the manifest schema.
func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			result = append(result, v)
		}
	}
	return result
}

// identifierMatches compares a search keyword against a set of correlation
// identifiers (ProductCodes, PackageFamilyNames, UpgradeCodes).
//
// MatchType Exact is deliberately treated case-insensitively for these fields,
// which fixes the installed-program correlation failures reported in
// https://github.com/microsoft/winget-cli-restsource/issues/166: the winget
// client builds every correlation search (winget list / post-install
// verification) from installed-ARP values that it runs through FoldCase (see
// CompositeSource.cpp SystemReferenceString), but sends them with MatchType
// Exact. A literal Exact comparison can therefore never match a manifest GUID
// written with uppercase letters. Upstream (denelon in
// winget-cli-restsource#166) confirmed there is "no requirement for a REST
// source to enforce a specific casing, which is why there is a Case Insensitive
// match"; treating Exact as folded for these identifier fields is what the
// official source's case-insensitive comparison achieves for clients that
// cannot change their request.
func identifierMatches(keywords []string, values []string, matchType string) bool {
	for _, value := range values {
		for _, keyword := range keywords {
			switch matchType {
			case "Exact":
				fallthrough
			case "CaseInsensitive":
				if strings.EqualFold(value, keyword) {
					return true
				}
			case "StartsWith":
				// StartsWith is implemented as case-sensitive, because it is that way in the reference implementation as well:
				// https://github.com/microsoft/winget-cli-restsource/blob/01542050d79da0efbd11c0a5be543cb970b86eb9/src/WinGet.RestSource/Cosmos/PredicateGenerator.cs#L92-L102
				if strings.HasPrefix(value, keyword) {
					return true
				}
			case "Substring":
				// Substring comparison is case-insensitive, because it is that way in the reference implementation as well:
				// https://github.com/microsoft/winget-cli-restsource/blob/01542050d79da0efbd11c0a5be543cb970b86eb9/src/WinGet.RestSource/Cosmos/PredicateGenerator.cs#L92-L102
				if caseInsensitiveContains(value, keyword) {
					return true
				}
			default:
				// Unimplemented match types (Wildcard, Fuzzy, FuzzySubstring)
				// intentionally do not match: silently accepting them would
				// report them as supported.
			}
		}
	}
	return false
}

// supportedSearchFields are the PackageMatchField values GetByMatchFilter
// actually evaluates. Anything else is reported to the client in
// UnsupportedPackageMatchFields instead of silently passing (see the
// searchFilterMatches doc comment).
//
// Deliberately NOT in this list: Command, Tag and Market. Until this change
// they went through findField, which looks for an exact struct field name - the
// API structs only have the plural Commands/Tags/Markets, so findField always
// returned an invalid reflect.Value and the field could never match. Under
// Filters (logical AND) that silently emptied the results of any search using
// them. The reference server implements Command/Tag against those plural
// fields; rewinged has no getters for them yet, so they are now reported in
// UnsupportedPackageMatchFields and ignored (the reference server likewise
// ignores a filter it cannot translate, e.g. Market) instead of quietly
// breaking the search. Implementing real Command/Tag matching is the proper
// follow-up fix.
var supportedSearchFields = map[string]bool{
	"NormalizedPackageNameAndPublisher": true,
	"PackageIdentifier":                 true,
	"PackageName":                       true,
	"Moniker":                           true,
	"PackageFamilyName":                 true,
	"ProductCode":                       true,
	// UpgradeCode is not in the published v1.x request schema enum, but the
	// reference server implements it (PredicateGenerator.GetInstallerClause:
	// AppsAndFeaturesEntries.UpgradeCode) and current clients never send it
	// (the CLI serializer drops the field), so this is forward support at zero
	// cost given the getter already exists.
	"UpgradeCode": true,
}

// strongMatchFields mirror the client's IsStrongMatchField (CompositeSource.cpp):
// fields that carry a definitive (non-heuristic) correlation signal. Used only
// for correlation disambiguation in GetByMatchFilter (see there).
var strongMatchFields = map[string]bool{
	"PackageFamilyName": true,
	"ProductCode":       true,
	"UpgradeCode":       true,
}

// normalizePackageName reduces a package name to the form the winget client
// puts in the wire KeyWord for NormalizedPackageNameAndPublisher inclusions.
//
// The client (winget-cli NameNormalization.cpp, NormalizationInitial) removes
// architecture/locale/version tokens, then splits on everything outside
// [\p{L}\p{Nd}\+&] and finally removes every character that is not a Unicode
// letter or digit (Remove(NonLettersAndDigits)). The surviving character class
// is what the wire keyword contains; verified from a live correlation request:
// manifest name "draw.io" arrives as KeyWord "drawio", and
// "Microsoft PC 状态检查" style non-ASCII names arrive with their accented
// letters intact (e.g. "windowspcállapotellenőrzés"), so the filter must be
// Unicode-aware, not ASCII-only.
//
// The bracket-enclosed removal mirrors the client's NonNestedBracket/
// BracketEnclosed stages, which run repeatedly until stable: "(R)", "(x86)"
// and "[Preview]" groups are dropped entirely, not just their brackets. The
// live-log Intel entry confirms it: ARP name "Intel(R) Graphics Software &
// Drivers" arrives as KeyWord "intelgraphicssoftwaredrivers", without the R.
//
// Token stripping: the client also removes version tokens before joining, and
// the Microsoft.DotNet.SDK.8 correlation proves it is load-bearing:
// PackageName "Microsoft .NET SDK 8.0" arrives on the wire as KeyWord
// "microsoftnetsdk" - the trailing "8.0" is stripped client-side, so the server
// must strip it too or the pair inclusion matches nothing and only the
// installed version (burn GUIDs are per-version) is ever found.
//
// Same mechanism, second log datum: "Microsoft Visual C++ v14 Redistributable
// (x64)" arrives as "microsoftvisualcredistributable", i.e. the bare "v14" is
// removed by the client's Version-prefix rule while "c" survives.
//
// Go's regexp is RE2 and has no lookbehind, so the ICU patterns from
// NameNormalization.cpp are decomposed into explicit boundary alternatives;
// trailing-boundary lookaheads like (?=\P{Nd}|$) cannot be expressed at all.
// Architecture and locale tokens are therefore not stripped as standalone
// rules: every log case carries them bracket-enclosed ("(x64)", "(x64 en-US)"),
// which the bracket pass already removes, and adding standalone rules would
// need lookahead or risk eating digits that the client keeps (the lone "11" in
// "Windows 11 Installation Assistant" survives as "windows11telepítésisegéd").
// A bare arch/locale token outside brackets stays a known gap.
var (
	bracketEnclosedRe = regexp.MustCompile(`\([^()]*\)|\[[^\[\]]*\]|"[^"]*"`)
	// kbNumberRe extracts patch identifiers from parentheses before the bracket
	// pass so they survive, as they do on the client.
	kbNumberRe = regexp.MustCompile(`\((kb\d+)\)`)
	// versionDelimitedRe: digits joined to further digit groups by punctuation
	// ("8.0", "154.0.4258.48", "26.03.00.0"). A lone number with no
	// punctuation digit group does NOT match: the log proves the client keeps
	// "11" in "Windows 11 Installation Assistant" and "2024" in
	// "Microsoft Office LTSC Professional Plus 2024" (keywords
	// "windows11telepítésisegéd", "...professionalplus2024proof").
	versionDelimitedRe = regexp.MustCompile(`(?:^|[^\p{L}\p{Nd}])(?:v|ver|versi(?:o|ó)n|versão|versie|wersja|build|release|rc|sp)\p{L}?\p{Nd}+(?:[\p{Po}\p{Pd}\p{Pc}]\p{Nd}?(?:rc|b|a|r|sp|k)?\p{Nd}+)+[\p{Po}\p{Pd}\p{Pc}]?[\p{L}\p{Nd}]*|(?:^|[^\p{L}\p{Nd}])\p{Nd}+(?:[\p{Po}\p{Pd}\p{Pc}]\p{Nd}?(?:rc|b|a|r|sp|k)?\p{Nd}+)+[\p{Po}\p{Pd}\p{Pc}]?[\p{L}\p{Nd}]*`)
	// versionWordRe: a version word led token, digits required ("v14",
	// "version 2.1", "build 17"). The live log pins "v14":
	// "Microsoft Visual C++ v14 Redistributable (x64)" arrives as
	// "microsoftvisualcredistributable" - the v14 goes, the c stays. Digits
	// being mandatory is what keeps the bare "v" of words like "visual" safe.
	versionWordRe = regexp.MustCompile(`(?:^|[^\p{L}])(?:p|v|r|ver|versi(?:o|ó)n|versão|versie|wersja|build|release|rc|sp)(?:\P{L}|\P{L}\p{L})?(?:\p{Nd}|\.\p{Nd})+(?:rc|b|a|r|v|sp)?\p{Nd}?`)
)

func normalizePackageName(name string) string {
	lower := strings.ToLower(name)
	lower = kbNumberRe.ReplaceAllString(lower, "$1")
	for {
		next := bracketEnclosedRe.ReplaceAllString(lower, "")
		if next == lower {
			break
		}
		lower = next
	}
	// Version rules run repeatedly until stable, as the client loops its
	// ProgramNameRegexes. Each pass either shortens the string or stops.
	for {
		next := versionDelimitedRe.ReplaceAllString(lower, "")
		next = versionWordRe.ReplaceAllString(next, "")
		if next == lower {
			break
		}
		lower = next
	}
	var b strings.Builder
	b.Grow(len(lower))
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// searchFilterMatches evaluates one Inclusions/Filters entry against one
// package version.
//
// Fields the server does not evaluate return (true, false) "matched but
// unsupported". The reference implementation is lenient the same way (its
// PredicateGenerator adds no clause for a field it cannot translate) and the
// official client consumes the UnsupportedPackageMatchFields response key to
// know the answer is less constrained than requested; refusing the whole
// request over one unmatchable field would break such a search outright. The
// unsupported flag is what makes the leniency visible to the client instead of
// a silent pass.
func searchFilterMatches(packageIdentifier string, packageVersion API_ManifestVersionInterface, filter API_SearchRequestPackageMatchFilter_1_1_0) (matched bool, supported bool) {
	if !supportedSearchFields[filter.PackageMatchField] {
		return true, false
	}

	switch filter.PackageMatchField {
	case "NormalizedPackageNameAndPublisher":
		// The REST search schema has only one KeyWord per match filter, so the
		// client (SearchRequestSerializer_1_0: "normalized-pair requests send
		// the name without the publisher") drops the publisher half of the
		// name+publisher pair on the wire. We can therefore only ever compare
		// against our own normalized PackageName. This is why the name+publisher
		// correlation remains imperfect: see
		// https://github.com/microsoft/winget-cli-restsource/issues/59
		//
		// Both sides go through normalizePackageName: our value because the
		// manifest stores the raw name, the keyword because a client-side
		// normalization change must not silently break Exact matching here.
		// The client sends this inclusion with MatchType Exact.
		normalized := normalizePackageName(packageVersion.GetDefaultLocalePackageName())
		normalizedFilter := filter
		normalizedFilter.RequestMatch.KeyWord = normalizePackageName(filter.RequestMatch.KeyWord)
		return compareSearchValue(normalized, normalizedFilter.RequestMatch), true
	case "PackageIdentifier":
		// We don't need to recursively search for this field, it's easy to get to
		requestMatchValue := packageIdentifier
		return compareSearchValue(requestMatchValue, filter.RequestMatch), true
	case "PackageFamilyName":
		// The getters replace findField here: the client's correlation search
		// set (CompositeSource.cpp) includes ARP-entry values, which only live
		// under AppsAndFeaturesEntries, and findField's first-match-wins scalar
		// lookup would silently pick just one installer-level value. It also
		// cannot resolve slice fields at all (reflect.Value.String() on a slice
		// returns a placeholder like "<[]string Value>" that can never match).
		return identifierMatches([]string{filter.RequestMatch.KeyWord}, packageVersion.GetInstallerPackageFamilyNames(), filter.RequestMatch.MatchType), true
	case "ProductCode":
		// The reference server matches installer ProductCode OR any
		// AppsAndFeaturesEntries ProductCode (PredicateGenerator.GetInstallerClause);
		// GetInstallerProductCodes collects both, mirroring the client's
		// Manifest::GetProductCodes correlation set.
		return identifierMatches([]string{filter.RequestMatch.KeyWord}, packageVersion.GetInstallerProductCodes(), filter.RequestMatch.MatchType), true
	case "UpgradeCode":
		return identifierMatches([]string{filter.RequestMatch.KeyWord}, packageVersion.GetInstallerUpgradeCodes(), filter.RequestMatch.MatchType), true
	case "PackageName":
		fallthrough
	case "Moniker":
		fallthrough
	default:
		// Just search the whole struct for a field with the right name
		// Get the value of a nested struct field passing in the field name to search for as a string
		// Source: https://stackoverflow.com/a/38407429
		//
		// findField can only resolve the first scalar string field with the
		// exact name. The correlation identifier fields (ProductCode,
		// PackageFamilyName, UpgradeCode) are handled above via the interface
		// getters because the client's correlation set also includes
		// AppsAndFeaturesEntries values, which findField's first-match-wins
		// lookup cannot reach.
		f := findField(packageVersion, string(filter.PackageMatchField))
		requestMatchValue := string(f.String())
		return compareSearchValue(requestMatchValue, filter.RequestMatch), true
	}
}

func compareSearchValue(requestMatchValue string, match API_SearchRequestMatch_1_1_0) bool {
	switch match.MatchType {
	case "Exact":
		return requestMatchValue == match.KeyWord
	case "CaseInsensitive":
		return strings.EqualFold(requestMatchValue, match.KeyWord)
	case "StartsWith":
		// StartsWith is implemented as case-sensitive, because it is that way in the reference implementation as well:
		// https://github.com/microsoft/winget-cli-restsource/blob/01542050d79da0efbd11c0a5be543cb970b86eb9/src/WinGet.RestSource/Cosmos/PredicateGenerator.cs#L92-L102
		return strings.HasPrefix(requestMatchValue, match.KeyWord)
	case "Substring":
		// Substring comparison is case-insensitive, because it is that way in the reference implementation as well:
		// https://github.com/microsoft/winget-cli-restsource/blob/01542050d79da0efbd11c0a5be543cb970b86eb9/src/WinGet.RestSource/Cosmos/PredicateGenerator.cs#L92-L102
		return caseInsensitiveContains(requestMatchValue, match.KeyWord)
	default:
		// Unimplemented match types (Wildcard, Fuzzy, FuzzySubstring)
		// intentionally do not match: silently accepting them would report
		// them as supported.
		return false
	}
}

// archLocaleTokenRe admits only tokens that plausibly denote an architecture
// or a locale code ("x64", "arm64", "msix", "hu", "en-US"). It deliberately
// rejects bracket groups that carry other content - "(R)", "(KB5034441)",
// "(2024)" - so a tie-break can never fire on arbitrary name decoration. The
// pattern mirrors the token families the client's NameNormalization.cpp removes
// (RemoveArchitecture/RemoveLocale), so tokens matching here are exactly the
// ones the client treats as arch/locale metadata.
var archLocaleTokenRe = regexp.MustCompile(`^(?:[a-z]{2,3}(?:-[a-z]{2,4})?|x86|x64|arm64?|msix)$`)

// bracketTokens extracts the arch/locale tokens from the bracket-enclosed
// groups of a raw (non-normalized) string: "mozilla firefox 159.0.1 (x64 en-us)"
// yields {x64, en-us}, "Mozilla Firefox (hu)" yields {hu}, and
// "Microsoft .NET SDK 8.0" yields {}.
func bracketTokens(raw string) map[string]bool {
	tokens := map[string]bool{}
	for _, group := range bracketEnclosedRe.FindAllString(raw, -1) {
		for _, token := range strings.Fields(group[1 : len(group)-1]) {
			token = strings.ToLower(token)
			if archLocaleTokenRe.MatchString(token) {
				tokens[token] = true
			}
		}
	}
	return tokens
}

// localeTokenTieBreak resolves a no-strong-anchor correlation fan-out when the
// request's bracketed arch/locale tokens (from the raw ARP/ProductCode keyword
// and pair keywords, before normalization strips them) intersect exactly one
// candidate package's own default-locale name tokens. Returns the winner's
// identifier, or "" when the ambiguity is not resolvable this way.
func localeTokenTieBreak(results map[string][]API_ManifestVersionInterface, requestFilters []API_SearchRequestPackageMatchFilter_1_1_0) string {
	requestTokens := map[string]bool{}
	for _, f := range requestFilters {
		for token := range bracketTokens(f.RequestMatch.KeyWord) {
			requestTokens[token] = true
		}
	}
	if len(requestTokens) == 0 {
		return ""
	}

	var winner string
	winners := 0
	for identifier, packageVersions := range results {
		// GetDefaultLocalePackageName is the raw name, bracket groups included -
		// the same value the search response serializes as PackageName.
		for token := range bracketTokens(packageVersions[0].GetDefaultLocalePackageName()) {
			if requestTokens[token] {
				winner = identifier
				winners++
				break
			}
		}
	}
	if winners == 1 {
		return winner
	}
	return ""
}

// guidShapeRe matches the brace-wrapped GUID form a wix/burn installer reports
// as ProductCode, which is also what AppsAndFeaturesEntries carry for an MSI
// install ("{6997c0ae-1f06-38b6-9645-72da2bbe5bf6}"). Bare-string ProductCodes
// like "Google Chrome" never match it.
var guidShapeRe = regexp.MustCompile(`(?i)^\{[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\}$`)

// guidShapeTieBreak resolves a no-strong-anchor correlation fan-out by
// install-technology identity: a brace-GUID ProductCode keyword means the
// installed app registered a wix/burn identifier, so only a package that
// itself declares GUID-shaped ProductCodes can be the installed one. The live
// Google.Chrome case: the installed GUID matches no manifest at all (recent
// Chrome releases moved to Google.Chrome.EXE, whose ProductCode is the bare
// string "Google Chrome"), both packages arrive only through the normalized
// pair ("Google Chrome" and "Google Chrome (EXE)" both normalize to
// "googlechrome"), and exactly one candidate - Google.Chrome - speaks
// GUID-shaped ProductCodes, the same conclusion the official source reaches by
// correlating the ARP GUID entry against Google.Chrome's own entries. A real
// Chrome.EXE install reports the bare string, the keyword is not GUID-shaped,
// and this rule cannot fire - that install keeps correlating through its exact
// ProductCode match. It runs only after the locale-token rule, inside a
// fan-out the client discards anyway, so it can only turn a certain loss into
// a win or leave the loss intact.
func guidShapeTieBreak(results map[string][]API_ManifestVersionInterface, requestFilters []API_SearchRequestPackageMatchFilter_1_1_0) string {
	requestIsGUID := false
	for _, f := range requestFilters {
		if f.PackageMatchField == "ProductCode" && guidShapeRe.MatchString(f.RequestMatch.KeyWord) {
			requestIsGUID = true
			break
		}
	}
	if !requestIsGUID {
		return ""
	}

	var winner string
	winners := 0
	for identifier, packageVersions := range results {
		for _, packageVersion := range packageVersions {
			declaresGUID := false
			for _, productCode := range packageVersion.GetInstallerProductCodes() {
				if guidShapeRe.MatchString(productCode) {
					declaresGUID = true
					break
				}
			}
			if declaresGUID {
				winner = identifier
				winners++
				break
			}
		}
		if winners > 1 {
			return ""
		}
	}
	if winners == 1 {
		return winner
	}
	return ""
}

// GetByMatchFilter returns the matching package versions, and the sorted set of
// PackageMatchField values from the request that this server does not evaluate
// (for reporting in UnsupportedPackageMatchFields).
func (ms *ManifestsStore) GetByMatchFilter(
	inclusions []API_SearchRequestPackageMatchFilter_1_1_0,
	filters []API_SearchRequestPackageMatchFilter_1_1_0,
) (
	map[string][]API_ManifestVersionInterface,
	[]string,
) {
	var manifestResultsMap = make(map[string][]API_ManifestVersionInterface)
	// packageStrong records packages reached through a strong identifier field
	// (see strongMatchFields), used for the correlation disambiguation below.
	packageStrong := make(map[string]bool)

	// Report every requested match field this server does not evaluate, per
	// request (not per version), independently of whether the inclusion loop
	// short-circuits before reaching it.
	unsupportedFields := make(map[string]bool)
	for _, f := range append(append([]API_SearchRequestPackageMatchFilter_1_1_0{}, filters...), inclusions...) {
		if !supportedSearchFields[f.PackageMatchField] {
			unsupportedFields[f.PackageMatchField] = true
		}
	}

	ms.RLock()
	for packageIdentifier, packageVersions := range ms.internal {
		// Loop through every version of the package as well because MatchFields like
		// ProductCode, PackageName etc. can change between versions.
	NEXT_VERSION:
		for _, packageVersion := range packageVersions {
			// versionStrong is set when this version is matched directly by a strong
			// identifier field (PFN/ProductCode/UpgradeCode), not only by a heuristic
			// field such as the normalized name pair.
			versionStrong := false

			// From what I can gather from https://github.com/microsoft/winget-cli-restsource/blob/01542050d79da0efbd11c0a5be543cb970b86eb9/src/WinGet.RestSource/Cosmos/CosmosDataStore.cs#L452
			// the difference between inclusions and filters are that inclusions are evaluated with a logical OR (only one of them has to match) and filters are evaluated with a logical AND
			// (all filter specified have to match) - so this is what I implemented here. But I am not 100% sure this is the correct/intended use for inclusions vs. filters.

			// process filters (if any)
			for _, filter := range filters {
				matched, _ := searchFilterMatches(packageIdentifier, packageVersion, filter)
				// Because all filters (if any) must match (logical AND)
				// we just skip to the next packageversion if any did not match
				if !matched {
					continue NEXT_VERSION
				}
				if strongMatchFields[filter.PackageMatchField] {
					versionStrong = true
				}
			}

			if len(inclusions) == 0 {
				manifestResultsMap[packageIdentifier] = append(manifestResultsMap[packageIdentifier], packageVersion)
				if versionStrong {
					packageStrong[packageIdentifier] = true
				}
				continue NEXT_VERSION
			}

			// process inclusions (if any): at least one must match (logical OR).
			// Every inclusion is evaluated (no early break) so a strong field later in
			// the list is still seen even when a weak field already satisfied the OR.
			var anyInclusionMatched bool
			for _, inclusion := range inclusions {
				matched, _ := searchFilterMatches(packageIdentifier, packageVersion, inclusion)
				if matched {
					anyInclusionMatched = true
					if strongMatchFields[inclusion.PackageMatchField] {
						versionStrong = true
					}
				}
			}

			// All filters and inclusions have passed for this manifest, add it to the returned map
			if anyInclusionMatched {
				logging.Logger.Debug().Msgf("adding to the results map: %v version %v", packageIdentifier, packageVersion.GetPackageVersion())
				manifestResultsMap[packageIdentifier] = append(manifestResultsMap[packageIdentifier], packageVersion)
				if versionStrong {
					packageStrong[packageIdentifier] = true
				}
			}
		}
	}
	ms.RUnlock()

	// Correlation disambiguation. The reference client keeps a multi-package
	// correlation response only when exactly one package matched on a strong
	// identifier field (CompositeSource.cpp: GetMatchingPackage ->
	// FindOnlyStrongMatchFieldResult, IsStrongMatchField = PFN/ProductCode/
	// UpgradeCode); otherwise it logs "Appropriate available package could not be
	// determined" and drops the whole result, which makes the installed app
	// disappear from `winget list` entirely. Crucially the client derives match
	// strength from Query/Filters only, never Inclusions (MatchCriteriaResolver.
	// cpp: GetBestMatchType), so on a REST source every correlation match arrives
	// with Unknown criteria and a multi-package response is always rejected - the
	// server must hand back a single package for correlation to work at all.
	//
	// NormalizedPackageNameAndPublisher is a heuristic field, and several distinct
	// packages share one normalized name in the community repo: "Microsoft .NET SDK
	// 8.0"/"9.0"/"10.0" all normalize to "microsoftnetsdk" (version tokens are
	// stripped, as the client does), and "Mozilla Firefox"/"(hu)"/"(de)"/"(MSIX)"
	// all collapse to "mozillafirefox" (bracket groups are dropped, as the client
	// does). The client cannot use that name to choose between them, so when the
	// fan-out leaves exactly one package that is also reached through a strong
	// identifier - the installed app's own wix ProductCode, a PackageFamilyName or
	// an UpgradeCode - we keep only that package, with all of its versions, so
	// upgrades still resolve. That is the decision FindOnlyStrongMatchFieldResult
	// makes once it can see the strength at all.
	//
	// When no package has a strong anchor, one signal on the wire can still
	// identify the installed variant: the ARP keyword itself carries architecture
	// and locale tokens in brackets ("mozilla firefox 159.0.1 (x64 en-us)"), and
	// community-repo locale siblings carry the same kind of token in their own
	// default-locale name ("Mozilla Firefox (hu)", "(MSIX)"). Exactly one
	// candidate whose name tokens intersect the request tokens wins (this is how
	// the official winget source lands on the base package for an en-US install);
	// if no token or several candidates match, the ambiguity is real and the
	// result is returned unchanged rather than guessed: nothing else on the wire
	// distinguishes "Microsoft .NET SDK 9.0" from "8.0" once both normalize to
	// the same keyword, and picking one would be a guess the client has no way
	// to confirm.
	//
	// ProductCode shape is a second wire-available signal when no token exists:
	// a brace-GUID keyword means a wix/burn install, so only a package whose own
	// ProductCodes are GUID-shaped can be it (installed Google Chrome GUID ->
	// Google.Chrome, never Google.Chrome.EXE with its bare "Google Chrome").
	//
	// This only collapses genuinely mixed-strength results. A search whose every
	// package matched through a strong field (e.g. Filters on ProductCode, which is
	// logical AND) has more than one strong package and is left intact.
	//
	// Cost, stated: the REST search request carries no search purpose, so this
	// cannot tell a correlation search apart from a user-issued multi-criteria
	// search that OR-es a strong identifier with a name (e.g. --product-code plus a
	// name criterion). Such a search gets narrowed to the strong-anchored package.
	// Correlation is the load-bearing case - it decides whether an installed app
	// appears in `winget list` at all - and plain `winget search` goes through
	// Query/GetByKeyword, which this does not touch.
	if len(manifestResultsMap) > 1 {
		var strongIdentifiers []string
		for identifier := range manifestResultsMap {
			if packageStrong[identifier] {
				strongIdentifiers = append(strongIdentifiers, identifier)
			}
		}

		switch {
		case len(strongIdentifiers) == 1:
			logging.Logger.Debug().Msgf("correlation disambiguation: %v package(s) matched, keeping the single strong-identifier match %v", len(manifestResultsMap), strongIdentifiers[0])
			manifestResultsMap = map[string][]API_ManifestVersionInterface{strongIdentifiers[0]: manifestResultsMap[strongIdentifiers[0]]}

		case len(strongIdentifiers) == 0:
			requestFilters := append(append([]API_SearchRequestPackageMatchFilter_1_1_0{}, inclusions...), filters...)
			tie := localeTokenTieBreak(manifestResultsMap, requestFilters)
			rule := "unique architecture/locale token"
			if tie == "" {
				tie = guidShapeTieBreak(manifestResultsMap, requestFilters)
				rule = "unique GUID-shaped ProductCode"
			}
			if tie != "" {
				logging.Logger.Debug().Msgf("correlation disambiguation: %v package(s) matched with no strong anchor, keeping %v on the %v", len(manifestResultsMap), tie, rule)
				manifestResultsMap = map[string][]API_ManifestVersionInterface{tie: manifestResultsMap[tie]}
			}
		}
	}

	// A strong identifier identifies a package, not one of its versions:
	// wix/burn ProductCodes are generated per release, so the installed app's
	// GUID only ever exists in the manifest version that shipped it. Returning
	// only matched versions would cap the response at the installed release and
	// `winget list` could never offer an upgrade - the live
	// Microsoft.DotNet.DesktopRuntime.10 case, where ProductCode
	// {F81499EB-06B3-470A-A4DB-D4B58AE6AEC2} exists only in 10.0.12 while newer
	// releases carry different GUIDs, and the ARP-derived pair keyword does not
	// match the manifest name either. The closed official source answers
	// correlation with whole packages (RestPackage keeps every Version in the
	// response and computes GetLatestVersion from that list), so a
	// strong-anchored package gets every version the store holds. That deviates
	// from the open restsource, whose CosmosDataStore returns matched
	// version-documents only - stated, deliberate: the goal is the official
	// source's outcome, not its document selection.
	for identifier := range manifestResultsMap {
		if packageStrong[identifier] {
			manifestResultsMap[identifier] = ms.GetAllVersions(identifier)
		}
	}

	var unsupported []string
	for field := range unsupportedFields {
		unsupported = append(unsupported, field)
	}
	sort.Strings(unsupported)
	if unsupported == nil {
		// keep the JSON wire shape consistent with RequiredPackageMatchFields ([] not null)
		unsupported = []string{}
	}

	return manifestResultsMap, unsupported
}

// This function takes two values and returns
// the one that's not set to its default value.
func nonDefault[T any](optionA T, optionB T) T {
	if isDefault(reflect.ValueOf(optionA)) {
		return optionB
	}
	return optionA
}

func isDefault(v reflect.Value) bool {
	return v.IsZero()
}

// This map is used to remember which InstallerSHAs were successfully
// internalized on manifest ingestion and can have their InstallerUrls rewritten.
var InternalizedInstallers = InternalizedInstallersStore{
	internal: make(map[string]bool),
}

// This map is used to remember the original InstallerUrl of installers that
// are eligible for on-demand internalization but not downloaded yet.
var InstallerSources = InstallerSourcesStore{
	internal: make(map[string]string),
}

// Global variable that will hold all in-memory manifest data
var Manifests = ManifestsStore{
	internal: make(map[string]map[string]API_ManifestVersionInterface),
}

package models

import (
    "sync"
    "sort"
    "strings"
    "reflect"

    "rewinged/logging"

    "gopkg.in/yaml.v3"
)

// This holds all BaseManifest information for easy access as well as
// a complete copy of the YAML document or node as ingested from disk.
type ManifestNode struct {
    BaseManifest `yaml:",inline"`
    Node yaml.Node `yaml:"-"`
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

func (ms *ManifestsStore) GetByKeyword (keyword string) map[string][]API_ManifestVersionInterface {
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

// normalizeReplacer strips the separators the (deliberately simple) normalized
// name comparison ignores. Package level so the hot search loop does not
// rebuild it per filter evaluation.
var normalizeReplacer = strings.NewReplacer(" ", "", "-", "", "+", "")

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
        requestMatchValue := normalizeReplacer.Replace(strings.ToLower(packageVersion.GetDefaultLocalePackageName()))
        return compareSearchValue(requestMatchValue, filter.RequestMatch), true
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

// GetByMatchFilter returns the matching package versions, and the sorted set of
// PackageMatchField values from the request that this server does not evaluate
// (for reporting in UnsupportedPackageMatchFields).
func (ms *ManifestsStore) GetByMatchFilter (
  inclusions []API_SearchRequestPackageMatchFilter_1_1_0,
  filters []API_SearchRequestPackageMatchFilter_1_1_0,
) (
  map[string][]API_ManifestVersionInterface,
  []string,
) {
  var manifestResultsMap = make(map[string][]API_ManifestVersionInterface)

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
      }

      if len(inclusions) == 0 {
        manifestResultsMap[packageIdentifier] = append(manifestResultsMap[packageIdentifier], packageVersion)
        continue NEXT_VERSION
      }

      // process inclusions (if any): at least one must match (logical OR)
      var anyInclusionMatched bool
      NEXT_INCLUSION:
      for _, inclusion := range inclusions {
        matched, _ := searchFilterMatches(packageIdentifier, packageVersion, inclusion)
        if matched {
          // Break out of the inclusions loop after one successful match
          anyInclusionMatched = true
          break NEXT_INCLUSION
        }
      }

      // All filters and inclusions have passed for this manifest, add it to the returned map
      if anyInclusionMatched {
        logging.Logger.Debug().Msgf("adding to the results map: %v version %v", packageIdentifier, packageVersion.GetPackageVersion())
        manifestResultsMap[packageIdentifier] = append(manifestResultsMap[packageIdentifier], packageVersion)
      }
    }
  }
  ms.RUnlock()

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
func nonDefault[T any] (optionA T, optionB T) T {
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


package controllers

import (
    "net/http"
    "encoding/json"

    "rewinged/models"
    "rewinged/settings"
)

func GetInformation(w http.ResponseWriter, r *http.Request) {
    // It's okay to always return the API 1.7.0+ version of the InformationSchema
    // because the only new property since 1.1.0, Authentication, is optional and
    // will be omitted if unset making the response identical to API 1.1.0
    response := new(models.API_Information_1_7_0)
    response.Data.SourceIdentifier = "rewinged"
    // New API schema versions have to be included here or winget CLI client won't pick
    // up the features / data fields from newer packages even if they are returned
    // NOTE: As of May 2026 there is no 1.12.0 API schema yet: https://github.com/microsoft/winget-cli-restsource/tree/main/documentation
    // NOTE: Also verified 2026-11-10: the newest published API schema is still 1.10.0. There is no
    // 1.12.0 or 1.28.0 API schema (WinGet-1.11.0.yaml / WinGet-1.12.0.yaml return 404 upstream), so
    // advertising 1.28.0 here would promise API semantics this server cannot deliver. Manifest
    // version 1.28.0 IS accepted on ingest (its only schema delta, DesiredStateConfiguration, has no
    // field in any published API schema) but such packages are served as API v1.10.0 responses.
    // NOTE: The API dialect 1.0.0 does exist upstream but is deliberately left out; the list below is
    // exactly the published REST specs minus 1.0.0 (verified 2026-11-11: WinGet-1.0.0/1.1.0/1.4.0/
    // 1.5.0/1.6.0/1.7.0/1.9.0/1.10.0 return 200, 1.2.0/1.3.0/1.8.0/1.11.0/1.12.0/1.28.0 return 404).
    // Adding it back would be a false promise, not a missing feature: rewinged does no per-request
    // dialect negotiation, so responses are always built from the API_* structs for 1.1.0 and up and
    // the client's Version header is only logged (controllers/packages.go), never acted on. Verified
    // with the real binary: requests sending Version: 1.0.0, 1.1.0, 1.10.0 or 9.9.9 all receive
    // byte-identical bodies. Responses also carry fields WinGet-1.0.0.yaml defines no schema for
    // (Agreements, ReleaseNotes, ExpectedReturnCodes, Markets, AppsAndFeaturesEntries, UpgradeCode,
    // ...), so advertising 1.0.0 claims a dialect this server does not speak. Revisit this only
    // together with real dialect negotiation.
    // This list is about API dialects, not manifest schema versions: manifest 1.0.0 IS accepted on
    // ingest and is served as an API v1.1.0 response (see manifests_1_0_0_test.go).
    response.Data.ServerSupportedVersions = []string{"1.1.0", "1.4.0", "1.5.0", "1.6.0", "1.7.0", "1.9.0", "1.10.0"}

    switch settings.SourceAuthenticationType {
    case "microsoftEntraId":
        // 1.7.0 is the minimum schema version that supports source-authentication
        response.Data.ServerSupportedVersions = []string{"1.7.0", "1.9.0", "1.10.0"}
        response.Data.Authentication = &models.API_Authentication_1_7_0{
            AuthenticationType: "microsoftEntraId",
            MicrosoftEntraIdAuthenticationInfo: struct {
                Resource string `yaml:"Resource"`
                Scope string `yaml:"Scope" json:",omitempty"`
            }{
                Resource: settings.SourceAuthenticationEntraIDResource, // Entra Application ID
                Scope: "user_impersonation",
            },
        }
    default:
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(response)
}


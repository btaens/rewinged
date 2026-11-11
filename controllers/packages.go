package controllers

import (
    "fmt"
    "strings"
    "net/http"
    "encoding/json"
    "sync"

    "rewinged/logging"
    "rewinged/settings"
    "rewinged/models"
)

func GetPackages(w http.ResponseWriter, r *http.Request) {
    response := &models.API_PackageMultipleResponse{
        Data: models.Manifests.GetAllPackageIdentifiers(),
    }

    logging.Logger.Debug().Msgf("%v", response)

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(response)
}

type GetPackageHandler struct {
    TlsEnabled bool
    InternalizationEnabled bool

    // mu serializes the installer-URL rewrite against the JSON response.
    // GetAllVersions hands out references to the manifests as stored in the
    // global Manifests store, and GetInstallers() returns pointers into those
    // stored structs, so rewriting InstallerUrl mutates shared state. Without
    // this lock, concurrent requests (a) race on that shared field and (b) can
    // hand a client a download URL derived from another client's request
    // headers, because the rewritten origin differs per request
    // (X-Forwarded-Host vs r.Host).
    mu sync.Mutex
}

func (h *GetPackageHandler) GetPackage(w http.ResponseWriter, r *http.Request) {
  logging.Logger.Debug().Msgf("/packageManifests: Someone tried to GET package '%v' with query params: %v", r.PathValue("package_identifier"), r.URL.Query())
  logging.Logger.Debug().Msgf("client requested API version %v", r.Header.Get("Version"))

  response := models.API_ManifestSingleResponse_1_1_0 {
    RequiredQueryParameters: []string{},
    UnsupportedQueryParameters: []string{},
    Data: nil,
  }

  var pkg []models.API_ManifestVersionInterface = models.Manifests.GetAllVersions(r.PathValue("package_identifier"))

  // Serialize the rewrite + response: see the comment on GetPackageHandler.mu.
  // Manifest responses are small and fetched only a few times per install, so
  // this serialization has no measurable cost, but it is what keeps the shared
  // installer metadata from being mutated under another request's feet.
  h.mu.Lock()
  defer h.mu.Unlock()

  if h.InternalizationEnabled {
      var rewrittenOrigin string
      // TODO: Only accept these headers if c.RemoteIP() is a trusted proxy configured by the user
      xfProto := r.Header.Values("X-Forwarded-Proto")
      xfHost  := r.Header.Values("X-Forwarded-Host")

      if len(xfProto) == 1 && xfProto[0] != "" && len(xfHost) == 1 && xfHost[0] != "" {
          rewrittenOrigin = fmt.Sprintf(
              "%s://%s", xfProto[0], xfHost[0],
          )
      } else {
          var proto string
          if h.TlsEnabled {
              proto = "https"
          } else {
              proto = "http"
          }
          rewrittenOrigin = fmt.Sprintf(
              "%s://%s", proto, r.Host,
          )
      }

      // We cannot use a range loop over the installers here because range loops
      // always put the current element in the loop into the same one memory address.
      // So if we collect/append the pointers to the installers in the loop, they
      // will all just point to the same memory location chosen by range.
      for i := 0; i < len(pkg); i++ {
          installers := pkg[i].GetInstallers()

          for j := 0; j < len(installers); j++ {
              // Only rewrite this installers InstallerUrl if it was marked for it on ingest
              if models.InternalizedInstallers.Get(installers[j].GetInstallerSha()) {
                  installers[j].SetInstallerUrl(
                      fmt.Sprintf(
                          "%s/installers/%s",
                          rewrittenOrigin,
                          strings.ToLower(installers[j].GetInstallerSha()),
                      ),
                  )

                  // If source authentication is configured, any internalized installers
                  // are also only downloadable with valid authentication. The winget client
                  // does not pass authentication along with the Installer download request
                  // unless the Installer metadata explicitly asks for it, which is only
                  // supported starting with API schema 1.10.0.
                  // This means two things IF authentication is enabled in rewinged:
                  //   1. Source (REST API) authentication was added in schema 1.7.0, but
                  //      InstallerAuthentication is only allowed with schema 1.10.0+. This
                  //      means, for AutoInternalized packages, for which rewinged hosts the
                  //      installer no schemas older than 1.10.0 can be used.
                  //   2. We have to explicitly edit the metadata of internalized installers
                  //      to say they will require authentication to download to make winget CLI
                  //      pass credentials with the download request
                  if settings.SourceAuthenticationType == "microsoftEntraId" {
                      // Currently hardcoded to insert a v1.10.0 Authentication struct, but it doesn't matter as the
                      // struct itself hasn't changed since being introduced in API 1.7.0 and it's only allowed in
                      // installers as of API 1.10.0. Technically, when adding support for the next API version after
                      // 1.10.0 this should type-switch on installers[j] and add the version-matched struct.
                      v, ok := installers[j].(models.API_InstallerWithAuthInterface[models.API_Authentication_1_10_0])
                      if ok {
                        v.SetInstallerAuthentication(&models.API_Authentication_1_10_0{
                          AuthenticationType: settings.SourceAuthenticationType,
                          MicrosoftEntraIdAuthenticationInfo: struct {
                            Resource string `yaml:"Resource"`
                            Scope string `yaml:"Scope" json:",omitempty"`
                          }{
                            Resource: settings.SourceAuthenticationEntraIDResource, // Entra Application ID
                            Scope: "user_impersonation",
                          },
                        })
                      } else {
                        logging.Logger.Warn().Msgf("manifest version of this package %T is too old and does not support InstallerAuthentication, client download will likely fail", installers[j])
                      }
                  }
              }
          }
      }
  }

  if len(pkg) > 0 {
    logging.Logger.Debug().Msgf("the package was found")

    response.Data = &models.API_Manifest_1_1_0{
      PackageIdentifier: r.PathValue("package_identifier"),
      Versions: pkg,
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(response)
  } else {
    logging.Logger.Debug().Msgf("the package was not found")
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusNotFound)
    json.NewEncoder(w).Encode(models.API_WingetApiError{
      ErrorCode: 404,
      ErrorMessage: "The specified package was not found.",
    })
  }
}

func SearchForPackage(w http.ResponseWriter, r *http.Request) {
  var post models.API_ManifestSearchRequest_1_1_0

  d := json.NewDecoder(r.Body)
  d.DisallowUnknownFields() // catch unwanted fields

  err := d.Decode(&post)
  if err != nil {
    // bad JSON or unrecognized json field
    logging.Logger.Error().Err(err).Msg("error deserializing json post body")
    http.Error(w, err.Error(), http.StatusBadRequest)
    return
  }

  logging.Logger.Debug().Msgf("%+v", post)
  // The search response version schema is served in its 1.4.0+ shape, which
  // adds UpgradeCodes and AppsAndFeaturesEntryVersions on top of the 1.1.0
  // fields. rewinged does not negotiate API dialects per request (see the
  // comment in controllers/information.go), and the winget client ignores
  // response keys its schema doesn't know, so the superset is safe for older
  // clients while clients >= 1.4 use these fields for installed-program
  // correlation (SearchResponseDeserializer_1_4 reads both).
  response := &models.API_ManifestSearchResult[models.API_ManifestSearchVersion_1_4_0]{
    RequiredPackageMatchFields: []string{},
    UnsupportedPackageMatchFields: []string{},
    Data: []models.API_ManifestSearchResponse[models.API_ManifestSearchVersion_1_4_0] {},
  }

  // results is a map where the PackageIdentifier is the key
  // and the values are arrays of manifests with that PackageIdentifier.
  // This means the values will be different versions of the package.
  var results map[string][]models.API_ManifestVersionInterface

  if post.Query.KeyWord != "" {
    logging.Logger.Debug().Msgf("someone searched the repo for: %v", post.Query.KeyWord)
    results = models.Manifests.GetByKeyword(post.Query.KeyWord)
  } else if len(post.Inclusions) > 0 || len(post.Filters) > 0 {
    logging.Logger.Debug().Msg("advanced search with inclusions[] and/or filters[]")
    var unsupported []string
    results, unsupported = models.Manifests.GetByMatchFilter(post.Inclusions, post.Filters)
    response.UnsupportedPackageMatchFields = unsupported
  }

  logging.Logger.Debug().Msgf("with %v results", len(results))

  if len(results) > 0 {
    for packageId, packageVersions := range results {
      logging.Logger.Debug().Msgf("package %v with %v versions", packageId, len(packageVersions))
      var versions []models.API_ManifestSearchVersion_1_4_0

      for _, version := range packageVersions {
        versions = append(versions, models.API_ManifestSearchVersion_1_4_0{
          PackageVersion: version.GetPackageVersion(),
          Channel: "",
          PackageFamilyNames: version.GetInstallerPackageFamilyNames(),
          ProductCodes: version.GetInstallerProductCodes(),
          AppsAndFeaturesEntryVersions: version.GetAppsAndFeaturesEntryVersions(),
          UpgradeCodes: version.GetInstallerUpgradeCodes(),
        })
      }

      response.Data = append(response.Data, models.API_ManifestSearchResponse[models.API_ManifestSearchVersion_1_4_0]{
        PackageIdentifier: packageId,
        PackageName: packageVersions[0].GetDefaultLocalePackageName(),
        Publisher: packageVersions[0].GetDefaultLocalePublisher(),
        Versions: versions,
      })
    }
    logging.Logger.Debug().Msgf("%+v", response)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(response)

  } else {
    // winget REST-API specification calls for a 204 return code if no results were found
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusNoContent)
    json.NewEncoder(w).Encode(response)
  }
}


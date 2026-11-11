package models

import (
    "fmt"
    "strconv"
    "strings"

    "gopkg.in/yaml.v3"
)

// InstallerReturnCode mirrors the schema definition of the same name
// (definitions/InstallerReturnCode in manifest.installer.*.json), which is the
// only integer-typed definition in the whole manifest schema surface. It is
// reached from exactly two fields: InstallerSuccessCodes and
// ExpectedReturnCodes[].InstallerReturnCode.
//
// The schema declares it as an integer, but real manifests in
// microsoft/winget-pkgs quote the value anyway. Verified upstream, verbatim:
//
//  InstallerSuccessCodes:
//    - "3010"
//    - "1641"
//
// (SonicPi.SonicPi, schema 1.1.0; ShareFile.ShareFileForWindows, schema 1.5.0.)
// yaml.v3 will not coerce a quoted scalar into a number, so a plain int64 field
// fails the whole file with `cannot unmarshal !!str "3010" into int64`, which in
// turn makes the package fail to load with "no (valid) installer manifest".
// Accepting both spellings keeps those packages loadable while the value stays
// an integer, so the REST API responses keep emitting numbers as the published
// API specs require (they declare InstallerReturnCode as `type: integer`).
//
// The schema additionally bounds the value to [-2147483648, 4294967295] and
// excludes 0. That is deliberately not enforced here: rewinged ingests
// manifests instead of policing them, and dropping a package over an out of
// range exit code is strictly worse than serving it.
type InstallerReturnCode int64

// UnmarshalYAML accepts a quoted decimal string in addition to everything
// yaml.v3 itself accepts for an unquoted integer.
//
// The two cases are handled differently on purpose. For an unquoted scalar the
// value is decoded by yaml.v3 into a plain int64, so the accepted spellings are
// exactly what a plain int64 field accepted before, with identical semantics:
// 0x hex, 0o and leading-zero octal, 0b binary and digit separators keep
// working (verified: 0x0BBA -> 3002, 010 -> 8, 3_010 -> 3010). Accepting quoted
// codes must not narrow anything else. For a quoted scalar the author wrote the
// digits as text, so it is read as plain decimal: quoted "010" means ten, not
// eight.
func (irc *InstallerReturnCode) UnmarshalYAML(node *yaml.Node) error {
    if node.Kind != yaml.ScalarNode {
        return fmt.Errorf("line %d: installer return code must be a number, got a %s", node.Line, node.ShortTag())
    }

    if node.Tag == "!!str" {
        parsed, err := strconv.ParseInt(strings.TrimSpace(node.Value), 10, 64)
        if err != nil {
            return fmt.Errorf("line %d: cannot parse %q as an installer return code", node.Line, node.Value)
        }
        *irc = InstallerReturnCode(parsed)
        return nil
    }

    // !!int, !!float and !!null: defer to yaml.v3 so the result is bit-for-bit
    // what this field produced while it was still a plain int64.
    var plain int64
    if err := node.Decode(&plain); err != nil {
        return err
    }
    *irc = InstallerReturnCode(plain)
    return nil
}

package updater

import "github.com/Masterminds/semver/v3"

// isNewer reports whether the candidate version is strictly newer than the
// current one using semantic-version ordering (github.com/Masterminds/semver).
// A leading "v" is tolerated and pre-release/build metadata is compared per the
// semver spec, so a stable "1.2.3" is correctly newer than "1.2.3-rc1". A
// current version of "dev" or "" (an unreleased local build) is always
// considered older so developers still see available updates. An unparsable
// candidate is never newer; an unparsable running version is treated as older
// so a valid release is still offered.
func isNewer(candidate, current string) bool {
	if candidate == "" {
		return false
	}
	if current == "" || current == "dev" {
		return true
	}

	cand, err := semver.NewVersion(candidate)
	if err != nil {
		return false
	}
	cur, err := semver.NewVersion(current)
	if err != nil {
		return true
	}
	return cand.GreaterThan(cur)
}

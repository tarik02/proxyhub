package proxyhub

import "golang.org/x/mod/semver"

func IsLegacyClientVersion(clientVersion string) bool {
	return semver.IsValid("v"+clientVersion) && semver.Compare("v"+clientVersion, "v2.0.0") < 0
}

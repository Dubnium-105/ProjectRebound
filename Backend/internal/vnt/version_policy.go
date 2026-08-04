package vnt

import "strings"

// VersionPolicy uses exact, case-sensitive matches so a node cannot advertise a
// merely similar build and accidentally enter the allocation pool.
type VersionPolicy struct {
	allowedVNTS    map[string]struct{}
	allowedWrapper map[string]struct{}
}

func NewVersionPolicy(vntsVersions, wrapperVersions []string) VersionPolicy {
	return VersionPolicy{
		allowedVNTS:    versionSet(vntsVersions),
		allowedWrapper: versionSet(wrapperVersions),
	}
}

func (p VersionPolicy) Compatible(node Node) bool {
	if len(p.allowedVNTS) == 0 || len(p.allowedWrapper) == 0 {
		return false
	}
	_, vntsAllowed := p.allowedVNTS[strings.TrimSpace(node.VNTSVersion)]
	_, wrapperAllowed := p.allowedWrapper[strings.TrimSpace(node.WrapperVersion)]
	return vntsAllowed && wrapperAllowed
}

func versionSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, raw := range values {
		if value := strings.TrimSpace(raw); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

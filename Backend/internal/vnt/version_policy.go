package vnt

import (
	"context"
	"strings"
	"sync"

	clientupdate "github.com/Dubnium-105/ProjectRebound/Backend/internal/update"
)

type versionPair struct {
	vnts    string
	wrapper string
}

// PublishedReleaseCatalog supplies runtime attestations extracted while
// validating published client releases. Publishing or rolling back a client
// release changes node eligibility without a configuration edit or restart.
type PublishedReleaseCatalog interface {
	PublishedVNTRuntimes(context.Context) ([]clientupdate.VNTRuntimeRelease, error)
}

// VersionSnapshot gives one request a consistent view of compatible runtime
// pairs and avoids a catalog/database lookup for every node in a list.
type VersionSnapshot struct {
	pairs map[versionPair]struct{}
}

func (s VersionSnapshot) Compatible(node Node) bool {
	return s.CompatibleVersions(node.VNTSVersion, node.WrapperVersion)
}

func (s VersionSnapshot) CompatibleVersions(vntsVersion, wrapperVersion string) bool {
	_, ok := s.pairs[versionPair{
		vnts: strings.TrimSpace(vntsVersion), wrapper: strings.TrimSpace(wrapperVersion),
	}]
	return ok
}

// VersionPolicy starts with the deployment values only as a startup fallback.
// Once SetPublishedCatalog is called, the published catalog is authoritative
// and an empty catalog fails closed.
type VersionPolicy struct {
	mu       sync.RWMutex
	fallback VersionSnapshot
	catalog  PublishedReleaseCatalog
}

func NewVersionPolicy(vntsVersions, wrapperVersions []string) *VersionPolicy {
	pairs := make(map[versionPair]struct{})
	for _, rawVNTS := range vntsVersions {
		vntsVersion := strings.TrimSpace(rawVNTS)
		if vntsVersion == "" {
			continue
		}
		for _, rawWrapper := range wrapperVersions {
			wrapperVersion := strings.TrimSpace(rawWrapper)
			if wrapperVersion != "" {
				pairs[versionPair{vnts: vntsVersion, wrapper: wrapperVersion}] = struct{}{}
			}
		}
	}
	return &VersionPolicy{fallback: VersionSnapshot{pairs: pairs}}
}

func (p *VersionPolicy) SetPublishedCatalog(catalog PublishedReleaseCatalog) {
	p.mu.Lock()
	p.catalog = catalog
	p.mu.Unlock()
}

func (p *VersionPolicy) Resolve(ctx context.Context) (VersionSnapshot, error) {
	if p == nil {
		return VersionSnapshot{pairs: map[versionPair]struct{}{}}, nil
	}
	p.mu.RLock()
	catalog := p.catalog
	fallback := p.fallback
	p.mu.RUnlock()
	if catalog == nil {
		return fallback, nil
	}
	runtimes, err := catalog.PublishedVNTRuntimes(ctx)
	if err != nil {
		return VersionSnapshot{}, err
	}
	pairs := make(map[versionPair]struct{})
	for _, runtime := range runtimes {
		vntsVersion := strings.TrimSpace(runtime.VNTSVersion)
		wrapperVersion := strings.TrimSpace(runtime.WrapperVersion)
		if vntsVersion != "" && wrapperVersion != "" {
			pairs[versionPair{vnts: vntsVersion, wrapper: wrapperVersion}] = struct{}{}
		}
	}
	return VersionSnapshot{pairs: pairs}, nil
}

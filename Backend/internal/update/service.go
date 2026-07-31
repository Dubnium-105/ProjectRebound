package update

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type RelayDirectory interface {
	AvailableRegions(context.Context) ([]string, error)
}

type ManagedCatalog interface {
	PublishedManifests(context.Context) ([]Manifest, error)
}

type Service struct {
	cfg       config.UpdateConfig
	signer    *Signer
	relay     RelayDirectory
	manifests []Manifest
	files     map[string]FileDownload
	managed   ManagedCatalog
}

func NewService(cfg config.UpdateConfig, environment string, relay RelayDirectory) (*Service, error) {
	signer, err := NewSigner(cfg, environment)
	if err != nil {
		return nil, err
	}
	manifests, files, err := loadCatalog(cfg, signer)
	if err != nil {
		if !(os.IsNotExist(err) && !strings.EqualFold(environment, "production")) {
			return nil, err
		}
		manifests = []Manifest{}
		files = make(map[string]FileDownload)
	}
	return &Service{cfg: cfg, signer: signer, relay: relay, manifests: manifests, files: files}, nil
}

func (s *Service) EphemeralSigner() bool { return s.signer.Ephemeral() }

func (s *Service) SetManagedCatalog(catalog ManagedCatalog) { s.managed = catalog }

func (s *Service) BuildAndSign(source SourceRelease) (Manifest, error) {
	baseURL, err := url.Parse(s.cfg.CDNBaseURL)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := buildManifest(s.cfg, baseURL, source)
	if err != nil {
		return Manifest{}, err
	}
	return s.signer.Sign(manifest)
}

func (s *Service) VerifySignedManifest(manifest Manifest) error {
	return s.signer.Verify(manifest)
}

func (s *Service) VerifyReleaseObjects(ctx context.Context, manifest Manifest) error {
	if len(manifest.Files) == 0 {
		return errors.New("release has no files to probe")
	}
	const (
		maxWorkers = 8
		probeTTL   = 10 * time.Second
	)
	probeCtx, cancel := context.WithTimeout(ctx, probeTTL)
	defer cancel()
	client := &http.Client{
		Timeout: probeTTL,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if len(via) == 0 {
				return nil
			}
			origin := via[0].URL
			if !strings.EqualFold(request.URL.Scheme, origin.Scheme) ||
				!strings.EqualFold(request.URL.Host, origin.Host) {
				return errors.New("cross-origin redirect rejected")
			}
			return nil
		},
	}
	jobs := make(chan File)
	workerCount := min(maxWorkers, len(manifest.Files))
	var (
		firstErr error
		errOnce  sync.Once
		workers  sync.WaitGroup
	)
	recordError := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-probeCtx.Done():
					return
				case file, ok := <-jobs:
					if !ok {
						return
					}
					request, err := http.NewRequestWithContext(probeCtx, http.MethodHead, file.DownloadURL, nil)
					if err != nil {
						recordError(fmt.Errorf("probe update object %q: %w", file.Path, err))
						return
					}
					response, err := client.Do(request)
					if err != nil {
						if response != nil {
							_ = response.Body.Close()
						}
						recordError(fmt.Errorf("probe update object %q: %w", file.Path, err))
						return
					}
					_ = response.Body.Close()
					if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
						recordError(fmt.Errorf("probe update object %q: unexpected HTTP status %d", file.Path, response.StatusCode))
						return
					}
				}
			}
		}()
	}
sendLoop:
	for _, file := range manifest.Files {
		select {
		case <-probeCtx.Done():
			break sendLoop
		case jobs <- file:
		}
	}
	close(jobs)
	workers.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := probeCtx.Err(); err != nil {
		return fmt.Errorf("probe update objects: %w", err)
	}
	return nil
}

func (s *Service) Check(ctx context.Context, input CheckInput) (CheckResult, error) {
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.Architecture = strings.ToLower(strings.TrimSpace(input.Architecture))
	input.Channel = strings.ToLower(strings.TrimSpace(input.Channel))
	input.Version = strings.TrimSpace(input.Version)
	if input.Architecture == "" {
		input.Architecture = s.cfg.DefaultArchitecture
	}
	if input.Channel == "" {
		input.Channel = s.cfg.DefaultChannel
	}
	if !identifierPattern.MatchString(input.Platform) || !identifierPattern.MatchString(input.Architecture) ||
		!validChannel(input.Channel) {
		return CheckResult{}, invalid("Platform, architecture, or channel is invalid.", nil)
	}
	if _, err := parseVersion(input.Version); err != nil {
		return CheckResult{}, invalid("Current version is invalid.", map[string]any{"version": input.Version})
	}
	var latest *Manifest
	manifests, _, err := s.catalog(ctx)
	if err != nil {
		return CheckResult{}, internal(err)
	}
	for index := range manifests {
		candidate := &manifests[index]
		if candidate.Platform != input.Platform || candidate.Architecture != input.Architecture || candidate.Channel != input.Channel {
			continue
		}
		if latest == nil {
			latest = candidate
			continue
		}
		comparison, _ := compareVersions(candidate.Version, latest.Version)
		if comparison > 0 {
			latest = candidate
		}
	}
	if latest == nil {
		return CheckResult{}, notFound("No release is available for the requested platform, architecture, and channel.")
	}
	latestComparison, _ := compareVersions(input.Version, latest.Version)
	minimumComparison, _ := compareVersions(input.Version, latest.MinimumSupportedVersion)
	manifestURL := fmt.Sprintf("/v1/updates/%s/%s/manifest?architecture=%s&channel=%s",
		url.PathEscape(latest.Platform), url.PathEscape(latest.Version),
		url.QueryEscape(latest.Architecture), url.QueryEscape(latest.Channel))
	return CheckResult{
		Product: latest.Product, Platform: latest.Platform, Architecture: latest.Architecture, Channel: latest.Channel,
		CurrentVersion: input.Version, LatestVersion: latest.Version, MinimumSupportedVersion: latest.MinimumSupportedVersion,
		UpdateAvailable: latestComparison < 0, UpdateRequired: minimumComparison < 0,
		PublishedAt: latest.PublishedAt, ManifestURL: manifestURL,
	}, nil
}

func (s *Service) Manifest(ctx context.Context, platform, architecture, channel, version string) (Manifest, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	architecture = strings.ToLower(strings.TrimSpace(architecture))
	channel = strings.ToLower(strings.TrimSpace(channel))
	version = strings.TrimSpace(version)
	if architecture == "" {
		architecture = s.cfg.DefaultArchitecture
	}
	if channel == "" {
		channel = s.cfg.DefaultChannel
	}
	manifests, _, err := s.catalog(ctx)
	if err != nil {
		return Manifest{}, internal(err)
	}
	for _, manifest := range manifests {
		if manifest.Platform == platform && manifest.Architecture == architecture && manifest.Channel == channel && manifest.Version == version {
			return manifest, nil
		}
	}
	return Manifest{}, notFound("Update manifest was not found.")
}

func (s *Service) File(ctx context.Context, fileID string) (FileDownload, error) {
	_, files, err := s.catalog(ctx)
	if err != nil {
		return FileDownload{}, internal(err)
	}
	file, ok := files[fileID]
	if !ok {
		return FileDownload{}, notFound("Update file was not found.")
	}
	return file, nil
}

func (s *Service) catalog(ctx context.Context) ([]Manifest, map[string]FileDownload, error) {
	byRelease := make(map[string]Manifest, len(s.manifests))
	for _, manifest := range s.manifests {
		byRelease[manifestCatalogKey(manifest)] = manifest
	}
	if s.managed != nil {
		managed, err := s.managed.PublishedManifests(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, manifest := range managed {
			if err := s.signer.Verify(manifest); err != nil {
				return nil, nil, fmt.Errorf("verify managed update manifest %s: %w", manifest.Version, err)
			}
			byRelease[manifestCatalogKey(manifest)] = manifest
		}
	}
	manifests := make([]Manifest, 0, len(byRelease))
	files := make(map[string]FileDownload)
	for _, manifest := range byRelease {
		manifests = append(manifests, manifest)
		for _, file := range manifest.Files {
			download := FileDownload{
				FileID: file.FileID, Size: file.Size, SHA256: file.SHA256, DownloadURL: file.DownloadURL,
			}
			if existing, duplicate := files[file.FileID]; duplicate && existing != download {
				return nil, nil, fmt.Errorf("file_id %q refers to multiple published objects", file.FileID)
			}
			files[file.FileID] = download
		}
	}
	sort.Slice(manifests, func(i, j int) bool {
		left, right := manifests[i], manifests[j]
		if left.Platform != right.Platform {
			return left.Platform < right.Platform
		}
		if left.Architecture != right.Architecture {
			return left.Architecture < right.Architecture
		}
		if left.Channel != right.Channel {
			return left.Channel < right.Channel
		}
		comparison, _ := compareVersions(left.Version, right.Version)
		return comparison < 0
	})
	return manifests, files, nil
}

func manifestCatalogKey(manifest Manifest) string {
	return manifest.Platform + "\x00" + manifest.Architecture + "\x00" +
		manifest.Channel + "\x00" + manifest.Version
}

func (s *Service) ClientConfig(ctx context.Context) (ClientConfig, error) {
	regions := []string{}
	if s.relay != nil {
		var err error
		regions, err = s.relay.AvailableRegions(ctx)
		if err != nil {
			return ClientConfig{}, internal(err)
		}
	}
	result := ClientConfig{
		APIVersion: s.cfg.APIVersion, ProtocolVersion: s.cfg.ProtocolVersion,
		MinimumClientVersion: s.cfg.MinimumClientVersion, RealtimeURL: s.cfg.RealtimeURL,
		STUNServers: append([]string(nil), s.cfg.STUNServers...),
	}
	for _, region := range regions {
		result.Relay.Regions = append(result.Relay.Regions, RelayRegion{ID: region, Available: true})
	}
	result.Relay.Available = len(result.Relay.Regions) > 0
	result.Features.P2PRooms = true
	result.Features.Relay = true
	result.Features.DedicatedServers = true
	return result, nil
}

func loadCatalog(cfg config.UpdateConfig, signer *Signer) ([]Manifest, map[string]FileDownload, error) {
	entries, err := os.ReadDir(cfg.ManifestDirectory)
	if err != nil {
		return nil, nil, err
	}
	baseURL, err := url.Parse(cfg.CDNBaseURL)
	if err != nil {
		return nil, nil, err
	}
	var manifests []Manifest
	files := make(map[string]FileDownload)
	seenReleases := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(cfg.ManifestDirectory, entry.Name())
		source, err := decodeSourceRelease(path)
		if err != nil {
			return nil, nil, fmt.Errorf("load update descriptor %s: %w", entry.Name(), err)
		}
		manifest, err := buildManifest(cfg, baseURL, source)
		if err != nil {
			return nil, nil, fmt.Errorf("validate update descriptor %s: %w", entry.Name(), err)
		}
		key := manifest.Platform + "\x00" + manifest.Architecture + "\x00" + manifest.Channel + "\x00" + manifest.Version
		if _, duplicate := seenReleases[key]; duplicate {
			return nil, nil, fmt.Errorf("duplicate update release %s/%s/%s/%s", manifest.Platform, manifest.Architecture, manifest.Channel, manifest.Version)
		}
		seenReleases[key] = struct{}{}
		manifest, err = signer.Sign(manifest)
		if err != nil {
			return nil, nil, err
		}
		for _, file := range manifest.Files {
			download := FileDownload{FileID: file.FileID, Size: file.Size, SHA256: file.SHA256, DownloadURL: file.DownloadURL}
			if existing, duplicate := files[file.FileID]; duplicate && existing != download {
				return nil, nil, fmt.Errorf("file_id %q refers to multiple objects", file.FileID)
			}
			files[file.FileID] = download
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool {
		left, right := manifests[i], manifests[j]
		if left.Platform != right.Platform {
			return left.Platform < right.Platform
		}
		if left.Architecture != right.Architecture {
			return left.Architecture < right.Architecture
		}
		if left.Channel != right.Channel {
			return left.Channel < right.Channel
		}
		comparison, _ := compareVersions(left.Version, right.Version)
		return comparison < 0
	})
	return manifests, files, nil
}

func decodeSourceRelease(path string) (SourceRelease, error) {
	file, err := os.Open(path)
	if err != nil {
		return SourceRelease{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(file, 1<<20)))
	decoder.DisallowUnknownFields()
	var source SourceRelease
	if err := decoder.Decode(&source); err != nil {
		return SourceRelease{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return SourceRelease{}, errors.New("descriptor must contain one JSON object")
	}
	return source, nil
}

func buildManifest(cfg config.UpdateConfig, baseURL *url.URL, source SourceRelease) (Manifest, error) {
	if source.SchemaVersion != 1 || source.Product != cfg.Product ||
		!identifierPattern.MatchString(source.Platform) || !identifierPattern.MatchString(source.Architecture) ||
		!validChannel(source.Channel) || source.PublishedAt.IsZero() || len(source.Files) == 0 {
		return Manifest{}, errors.New("release metadata is invalid")
	}
	if _, err := parseVersion(source.Version); err != nil {
		return Manifest{}, fmt.Errorf("version: %w", err)
	}
	if _, err := parseVersion(source.MinimumSupportedVersion); err != nil {
		return Manifest{}, fmt.Errorf("minimum_supported_version: %w", err)
	}
	if comparison, _ := compareVersions(source.MinimumSupportedVersion, source.Version); comparison > 0 {
		return Manifest{}, errors.New("minimum_supported_version cannot exceed version")
	}
	manifest := Manifest{
		SchemaVersion: source.SchemaVersion, Product: source.Product,
		Platform: strings.ToLower(source.Platform), Architecture: strings.ToLower(source.Architecture), Channel: source.Channel,
		Version: source.Version, MinimumSupportedVersion: source.MinimumSupportedVersion,
		PublishedAt: source.PublishedAt.UTC(), Files: make([]File, 0, len(source.Files)),
	}
	seenPaths := make(map[string]struct{})
	for _, sourceFile := range source.Files {
		cleanPath := path.Clean(sourceFile.Path)
		if !identifierPattern.MatchString(sourceFile.FileID) || sourceFile.Size < 0 || !sha256Pattern.MatchString(sourceFile.SHA256) ||
			strings.Contains(sourceFile.Path, "\\") || strings.HasPrefix(cleanPath, "/") ||
			(cleanPath != sourceFile.Path && cleanPath != strings.TrimPrefix(sourceFile.Path, "./")) || cleanPath == "." || strings.HasPrefix(cleanPath, "../") ||
			(sourceFile.Compression != "none" && sourceFile.Compression != "gzip" && sourceFile.Compression != "zstd") {
			return Manifest{}, fmt.Errorf("invalid file entry %q", sourceFile.Path)
		}
		if _, duplicate := seenPaths[cleanPath]; duplicate {
			return Manifest{}, fmt.Errorf("duplicate file path %q", cleanPath)
		}
		seenPaths[cleanPath] = struct{}{}
		downloadURL, err := objectURL(baseURL, sourceFile.ObjectKey)
		if err != nil {
			return Manifest{}, fmt.Errorf("file %q: %w", cleanPath, err)
		}
		manifest.Files = append(manifest.Files, File{
			FileID: sourceFile.FileID, Path: cleanPath, Size: sourceFile.Size, SHA256: sourceFile.SHA256,
			Compression: sourceFile.Compression, DownloadURL: downloadURL,
		})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	return manifest, nil
}

func objectURL(base *url.URL, objectKey string) (string, error) {
	objectKey = strings.Trim(objectKey, "/")
	if objectKey == "" {
		return "", errors.New("object_key is required")
	}
	segments := strings.Split(objectKey, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("object_key contains an unsafe segment")
		}
		segments[index] = url.PathEscape(segment)
	}
	resolved := *base
	resolved.RawQuery = ""
	resolved.Fragment = ""
	resolved.Path = strings.TrimRight(base.Path, "/") + "/" + strings.Join(segments, "/")
	return resolved.String(), nil
}

func validChannel(value string) bool {
	switch value {
	case ChannelStable, ChannelBeta, ChannelToolbox:
		return true
	default:
		return false
	}
}

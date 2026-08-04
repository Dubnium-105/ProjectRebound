package update

import "time"

const (
	SignatureAlgorithm = "Ed25519"
	ChannelStable      = "stable"
	ChannelBeta        = "beta"
	ChannelToolbox     = "toolbox"
)

type File struct {
	FileID      string `json:"file_id"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Compression string `json:"compression"`
	DownloadURL string `json:"download_url"`
}

type Manifest struct {
	SchemaVersion           int       `json:"schema_version"`
	Product                 string    `json:"product"`
	Platform                string    `json:"platform"`
	Architecture            string    `json:"architecture"`
	Channel                 string    `json:"channel"`
	Version                 string    `json:"version"`
	MinimumSupportedVersion string    `json:"minimum_supported_version"`
	PublishedAt             time.Time `json:"published_at"`
	Files                   []File    `json:"files"`
	ManifestHash            string    `json:"manifest_hash"`
	SignatureAlgorithm      string    `json:"signature_algorithm"`
	KeyID                   string    `json:"key_id"`
	Signature               string    `json:"signature"`
}

type SourceRelease struct {
	SchemaVersion           int          `json:"schema_version"`
	Product                 string       `json:"product"`
	Platform                string       `json:"platform"`
	Architecture            string       `json:"architecture"`
	Channel                 string       `json:"channel"`
	Version                 string       `json:"version"`
	MinimumSupportedVersion string       `json:"minimum_supported_version"`
	PublishedAt             time.Time    `json:"published_at"`
	Files                   []SourceFile `json:"files"`
}

type SourceFile struct {
	FileID      string `json:"file_id"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Compression string `json:"compression"`
	ObjectKey   string `json:"object_key"`
}

type CheckInput struct {
	Platform     string
	Architecture string
	Channel      string
	Version      string
}

type CheckResult struct {
	Product                 string    `json:"product"`
	Platform                string    `json:"platform"`
	Architecture            string    `json:"architecture"`
	Channel                 string    `json:"channel"`
	CurrentVersion          string    `json:"current_version"`
	LatestVersion           string    `json:"latest_version"`
	MinimumSupportedVersion string    `json:"minimum_supported_version"`
	UpdateAvailable         bool      `json:"update_available"`
	UpdateRequired          bool      `json:"update_required"`
	PublishedAt             time.Time `json:"published_at"`
	ManifestURL             string    `json:"manifest_url"`
}

type FileDownload struct {
	FileID      string `json:"file_id"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

type RelayRegion struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
}

type ClientConfig struct {
	APIVersion           string   `json:"api_version"`
	ProtocolVersion      int      `json:"protocol_version"`
	MinimumClientVersion string   `json:"minimum_client_version"`
	RealtimeURL          string   `json:"realtime_url"`
	STUNServers          []string `json:"stun_servers"`
	Relay                struct {
		Available bool          `json:"available"`
		Regions   []RelayRegion `json:"regions"`
	} `json:"relay"`
	Features struct {
		P2PRooms         bool `json:"p2p_rooms"`
		Relay            bool `json:"relay"`
		DedicatedServers bool `json:"dedicated_servers"`
		VNTRooms         bool `json:"vnt_rooms"`
	} `json:"features"`
}

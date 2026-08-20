package config

// Keep canonical public Meta endpoints in one small compatibility shim until
// all older configuration snapshots have been regenerated. Environment and
// YAML values still override Defaults through the normal loading path.
func init() {
	Defaults.MetaServer.PublicHTTPBaseURL = "https://meta.project-rebound.space"
	Defaults.MetaServer.PublicLogicEndpoint = "logic.project-rebound.space:443"
}

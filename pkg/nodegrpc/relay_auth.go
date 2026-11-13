package nodegrpc

import "regexp"

const RelayAuthPathPrefix = "/yagp/relay-auth/"

var relayAuthService = regexp.MustCompile(`^relay-tunnel-[1-9][0-9]*$`)

func IsRelayAuthService(name string) bool {
	return relayAuthService.MatchString(name)
}

// RelayAuthConfig is sent only over the authenticated panel-to-daemon session.
type RelayAuthConfig struct {
	Users []RelayAuthUser `json:"users"`
}

type RelayAuthUser struct {
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	AllowedIPs      []string `json:"allowed_ips,omitempty"`
	RestrictSources bool     `json:"restrict_sources,omitempty"`
}

type RelayAuthStatus struct {
	Configured bool `json:"configured"`
}

package nodegrpc

import (
	"errors"
	"net/netip"
	"regexp"
	"strings"
)

const PanelPort = "443"

type PanelHost struct{ host string }

var panelDNSLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func ParsePanelHost(host string) (PanelHost, error) {
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" {
		return PanelHost{host: address.String()}, nil
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 {
		return PanelHost{}, errors.New("panel host name exceeds 253 bytes")
	}
	for _, label := range strings.Split(host, ".") {
		if !panelDNSLabel.MatchString(label) {
			return PanelHost{}, errors.New("panel host must be a bare DNS name or IP address")
		}
	}
	return PanelHost{host: strings.ToLower(host)}, nil
}

func (h PanelHost) Host() string { return h.host }

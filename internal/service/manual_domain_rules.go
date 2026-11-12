package service

import (
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var (
	manualDomainLabel   = regexp.MustCompile(`^[a-z0-9*?]([a-z0-9*?-]{0,61}[a-z0-9*?])?$`)
	errManualDomainPort = errors.New("端口须为 0 到 65535 的数字，或起始端口不大于结束端口的范围，如 443、400-500")
)

func normalizeManualDomainRule(value string) (string, error) {
	host, port, hasPort := strings.Cut(strings.ToLower(strings.TrimSpace(value)), ":")
	host = strings.TrimSuffix(host, ".")
	domain := strings.TrimPrefix(host, ".")
	if len(domain) > 253 {
		return "", errors.New("域名不能超过 253 个字符")
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return "", errors.New("请填写域名或域名通配规则，不能填写 IP 地址")
	}
	if strings.HasPrefix(host, ".") && strings.ContainsAny(domain, "*?") {
		return "", errors.New("通配规则不能同时使用前导点，请去掉前导点")
	}
	for _, label := range strings.Split(domain, ".") {
		if !manualDomainLabel.MatchString(label) {
			return "", errors.New("请填写域名、.域名或包含 *、? 的域名规则，国际化域名使用 Punycode")
		}
	}
	if !hasPort {
		return host, nil
	}
	port, err := normalizeManualDomainPort(port)
	if err != nil {
		return "", err
	}
	return host + ":" + port, nil
}

func normalizeManualDomainPort(value string) (string, error) {
	// Gost treats an invalid port as unrestricted, so reject it before saving the matcher.
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return "", errManualDomainPort
	}
	var previous uint64
	for i, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return "", errManualDomainPort
			}
		}
		port, err := strconv.ParseUint(part, 10, 16)
		if err != nil || (i > 0 && port < previous) {
			return "", errManualDomainPort
		}
		parts[i], previous = strconv.FormatUint(port, 10), port
	}
	return strings.Join(parts, "-"), nil
}

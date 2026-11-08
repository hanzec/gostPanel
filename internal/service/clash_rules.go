package service

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

var (
	clashDNSLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	clashKeyword  = regexp.MustCompile(`^[a-z0-9.-]+$`)
	clashRuleType = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,31}$`)
	errClashYAML  = errors.New("expected a single YAML document containing a payload list of rules")
)

// ParseClashRules converts a Clash rule provider to Gost destination matchers.
func ParseClashRules(data []byte) ([]string, error) {
	source := strings.TrimPrefix(string(data), "\uFEFF")
	if !utf8.ValidString(source) || strings.IndexByte(source, 0) >= 0 {
		return nil, errors.New("the rule set must be UTF-8 YAML or text; export binary rule sets to text first")
	}
	entries, err := clashRuleEntries(source)
	if err != nil {
		return nil, err
	}
	matchers := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		matcher, err := clashMatcher(entry.value)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", entry.line, err)
		}
		if _, exists := seen[matcher]; !exists {
			seen[matcher] = struct{}{}
			matchers = append(matchers, matcher)
		}
	}
	if len(matchers) == 0 {
		return nil, errors.New("the rule set has no destination rules")
	}
	return matchers, nil
}

type clashRuleEntry struct {
	line  int
	value string
}

func clashRuleEntries(source string) ([]clashRuleEntry, error) {
	var entries []clashRuleEntry
	for index, line := range strings.Split(source, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(entries) == 0 {
			key, _, mapping := strings.Cut(line, ":")
			if line == "---" || strings.HasPrefix(line, "%YAML ") || strings.HasPrefix(line, "{") ||
				(mapping && strings.Trim(strings.TrimSpace(key), `"'`) == "payload") {
				return clashYAMLEntries(source)
			}
		}
		entries = append(entries, clashRuleEntry{line: index + 1, value: line})
	}
	return entries, nil
}

func clashYAMLEntries(source string) ([]clashRuleEntry, error) {
	var document, extra yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if decoder.Decode(&document) != nil || decoder.Decode(&extra) != io.EOF || len(document.Content) != 1 {
		return nil, errClashYAML
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || len(root.Content) != 2 || root.Content[0].Value != "payload" || root.Content[1].Kind != yaml.SequenceNode {
		return nil, errClashYAML
	}
	entries := make([]clashRuleEntry, 0, len(root.Content[1].Content))
	for _, node := range root.Content[1].Content {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || strings.ContainsAny(node.Value, "\r\n") {
			return nil, fmt.Errorf("line %d: each payload entry must be one rule string", node.Line)
		}
		entries = append(entries, clashRuleEntry{line: node.Line, value: strings.TrimSpace(node.Value)})
	}
	return entries, nil
}

func clashMatcher(rule string) (string, error) {
	kind, value, classical := strings.Cut(rule, ",")
	if !classical {
		return clashBareMatcher(rule)
	}
	kind = strings.ToUpper(strings.TrimSpace(kind))
	value, option, hasOption := strings.Cut(value, ",")
	value = strings.TrimSpace(value)
	switch kind {
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6":
	default:
		if clashRuleType.MatchString(kind) {
			return "", fmt.Errorf("unsupported Clash rule type %s", kind)
		}
		return "", errors.New("expected a Clash rule type before the comma")
	}
	cidr := kind == "IP-CIDR" || kind == "IP-CIDR6"
	if hasOption && !(cidr && strings.EqualFold(strings.TrimSpace(option), "no-resolve")) {
		return "", fmt.Errorf("%s has extra fields; use rules from a provider without an outbound policy", kind)
	}
	if cidr {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return "", fmt.Errorf("%s requires a valid IPv4 or IPv6 CIDR", kind)
		}
		return prefix.Masked().String(), nil
	}
	if kind == "DOMAIN-KEYWORD" {
		keyword := strings.ToLower(value)
		if !clashKeyword.MatchString(keyword) || strings.Trim(keyword, "0123456789abcdef.") == "" {
			return "", errors.New("DOMAIN-KEYWORD must be a literal ASCII keyword that cannot match a numeric IP address")
		}
		return "*" + keyword + "*", nil
	}
	if kind == "DOMAIN-SUFFIX" {
		return clashDomainMatcher(value, ".")
	}
	return clashDomainMatcher(value, "")
}

func clashBareMatcher(value string) (string, error) {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked().String(), nil
	}
	if address, err := netip.ParseAddr(value); err == nil && address.Zone() == "" {
		return address.String(), nil
	}
	if domain, ok := strings.CutPrefix(value, "+."); ok {
		return clashDomainMatcher(domain, ".")
	}
	if domain, ok := strings.CutPrefix(value, "."); ok {
		return clashDomainMatcher(domain, "*.")
	}
	if strings.ContainsAny(value, "*?") {
		return "", errors.New("Clash single-label wildcards cannot be represented by Gost; use exact names or a +.domain suffix")
	}
	return clashDomainMatcher(value, "")
}

func clashDomainMatcher(value, prefix string) (string, error) {
	domain := strings.ToLower(strings.TrimSuffix(value, "."))
	if len(domain) > 253 {
		return "", errors.New("domain exceeds 253 characters")
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return "", errors.New("domain rule contains an IP address; use an IP or CIDR entry")
	}
	for _, label := range strings.Split(domain, ".") {
		if !clashDNSLabel.MatchString(label) {
			return "", errors.New("expected a hostname, IP address or CIDR; write internationalized domains in Punycode")
		}
	}
	return prefix + domain, nil
}

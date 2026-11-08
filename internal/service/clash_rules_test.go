package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseClashRules(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  []string
	}{
		{
			name: "YAML provider",
			input: `# rules for the proxy
payload:
  - DOMAIN,Exact.Example
  - DOMAIN-SUFFIX,example.net
  - DOMAIN-KEYWORD,git-hub
  - IP-CIDR,198.51.100.127/24,no-resolve
  - IP-CIDR6,2001:db8::42/32
  - '+.example.net'
  - '.subdomain-only.example'
`,
			want: []string{"exact.example", ".example.net", "*git-hub*", "198.51.100.0/24", "2001:db8::/32", "*.subdomain-only.example"},
		},
		{
			name:  "text with BOM, CRLF and comments",
			input: "\uFEFF# provider\r\n192.0.2.1\r\n2001:db8::1 # literal IPv6\r\nDOMAIN-SUFFIX,test.example\r\n+.test.example\r\n",
			want:  []string{"192.0.2.1", "2001:db8::1", ".test.example"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseClashRules([]byte(tt.input))
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseClashRules() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestParseClashRulesRejectsUnrepresentableRules(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
	}{
		{"DOMAIN,example.com\nGEOSITE,cn", "line 2: unsupported Clash rule type GEOSITE"},
		{"payload:\n  - '*.example.com'", "line 2: Clash single-label wildcards"},
		{"IP-CIDR,192.0.2.0/24,PROXY,no-resolve", "without an outbound policy"},
		{"IP-CIDR,not-an-address", "valid IPv4 or IPv6 CIDR"},
		{"payload:\n  - 1234", "line 2: each payload entry must be one rule string"},
		{"payload: [example.com]\n---\npayload: [second.example.com]", "single YAML document"},
		{"\x00\xff", "export binary rule sets to text"},
		{"# only a comment", "no destination rules"},
	} {
		got, err := ParseClashRules([]byte(tt.input))
		if got != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ParseClashRules(%q) = %v, %v; want no partial matchers and error containing %q", tt.input, got, err, tt.want)
		}
	}
}

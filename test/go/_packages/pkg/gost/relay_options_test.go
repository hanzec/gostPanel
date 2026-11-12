package gost

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRelayOptionsWriteGostListenerAndDialerJSON(t *testing.T) {
	options := map[string]string{
		"path": "/custom/relay", "backlog": "64", "compression": "false",
		"host": "relay.example", "request_headers": `{"X-Client":"panel"}`,
		"response_headers": `{"X-Relay":"exit"}`,
		"tls_server_name":  "relay.example", "tls_verify": "true",
		"tls_server_cert_file": "/etc/gost/server.crt", "tls_server_key_file": "/etc/gost/server.key",
		"tls_min_version": "VersionTLS12", "tls_alpn": "http/1.1",
	}
	if err := ValidateRelayOptions("wss", options); err != nil {
		t.Fatal(err)
	}
	exit := RelayListener("wss", options)
	entry := RelayDialer("wss", options)
	checkRelayJSON(t, exit, `{
		"type":"wss",
		"metadata":{"ws.path":"/custom/relay","ws.backlog":"64","ws.enableCompression":"false","ws.header":{"X-Relay":"exit"}},
		"tls":{"certFile":"/etc/gost/server.crt","keyFile":"/etc/gost/server.key","options":{"minVersion":"VersionTLS12","alpn":["http/1.1"]}}
	}`)
	checkRelayJSON(t, entry, `{
		"type":"wss",
		"metadata":{"ws.path":"/custom/relay","ws.enableCompression":"false","ws.host":"relay.example","ws.header":{"X-Client":"panel"}},
		"tls":{"serverName":"relay.example","secure":true,"options":{"minVersion":"VersionTLS12","alpn":["http/1.1"]}}
	}`)
	checkRelayJSON(t, RelayListener("wss", nil), `{"type":"wss"}`)
	checkRelayJSON(t, RelayDialer("wss", map[string]string{}), `{"type":"wss"}`)
}

func TestRelayKCPOverridesUseTypedNestedFieldsAndPreserveDefaults(t *testing.T) {
	options := map[string]string{
		"kcp_mode": "manual", "kcp_interval": "20", "kcp_dscp": "0",
		"kcp_data_shards": "7", "kcp_ack_no_delay": "true", "kcp_no_compression": "false",
	}
	if err := ValidateRelayOptions("kcp", options); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"kcp","metadata":{
		"kcp.mode":"manual","kcp.interval":"20","kcp.nocomp":"false",
		"kcp.config":{"datashard":7,"parityshard":3,"dscp":0,"sockbuf":4194304,"snmpperiod":60,"acknodelay":true}
	}}`
	checkRelayJSON(t, RelayListener("kcp", options), want)
	checkRelayJSON(t, RelayDialer("kcp", options), want)
}

func TestRelayGRPCAuthorityAndTLSNameAgree(t *testing.T) {
	for _, options := range []map[string]string{
		{"host": "relay.example:443", "grpc_service": "/panel.Relay/Stream"},
		{"tls_server_name": "relay.example", "grpc_service": "/panel.Relay/Stream"},
	} {
		if err := ValidateRelayOptions("grpc", options); err != nil {
			t.Fatal(err)
		}
		entry := RelayDialer("grpc", options)
		authority, _ := entry.Metadata["grpc.authority"].(string)
		if grpcAuthority(authority) != "relay.example" || entry.TLS["serverName"] != "relay.example" ||
			RelayListener("grpc", options).Metadata["grpc.path"] != "/panel.Relay/Stream" {
			t.Fatalf("gRPC config disagrees: %+v", entry)
		}
	}
	plaintext := map[string]string{"host": "relay.example", "grpc_insecure": "true"}
	checkRelayJSON(t, RelayDialer("grpc", plaintext), `{"type":"grpc","metadata":{"grpc.authority":"relay.example","grpc.insecure":"true"}}`)
}

func TestRelayRejectsOptionsThatGostCannotApply(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		options        map[string]string
		want           string
	}{
		{"different protocol", "tcp", map[string]string{"path": "/relay"}, "不支持"},
		{"raw metadata", "wss", map[string]string{"preUp": "unexpected"}, "不支持"},
		{"empty", "wss", map[string]string{"host": ""}, "非空"},
		{"multiline", "wss", map[string]string{"host": "relay\n.example"}, "单行"},
		{"invalid boolean", "wss", map[string]string{"compression": "on"}, "启用或禁用"},
		{"invalid integer", "quic", map[string]string{"max_streams": "1.5"}, "整数"},
		{"integer range", "quic", map[string]string{"max_streams": "0"}, "整数"},
		{"invalid duration", "quic", map[string]string{"max_idle_timeout": "-2s"}, "时长"},
		{"invalid enum", "mtcp", map[string]string{"mux_version": "3"}, "受支持"},
		{"invalid path", "ws", map[string]string{"path": "/relay?other=1"}, "不带查询"},
		{"invalid file", "tls", map[string]string{"tls_ca_file": "ca.pem"}, "绝对路径"},
		{"invalid headers", "ws", map[string]string{"request_headers": "[]"}, "JSON"},
		{"invalid header value", "ws", map[string]string{"request_headers": `{"X-Test":null}`}, "HTTP 头"},
		{"protected header", "pht", map[string]string{"request_headers": `{"Host":"relay.example"}`}, "由通道处理"},
		{"protected upgrade header", "ws", map[string]string{"request_headers": `{"Sec-WebSocket-Key":"bad"}`}, "握手头"},
		{"empty list item", "tls", map[string]string{"tls_alpn": "h2,"}, "逗号"},
		{"invalid cipher suite", "tls", map[string]string{"tls_cipher_suites": "TLS_AES_128_GCM_SHA256"}, "TLS 1.2"},
		{"invalid grpc name", "grpc", map[string]string{"grpc_service": "not a service"}, "gRPC"},
		{"invalid quic AES key", "quic", map[string]string{"quic_cipher_key": "short"}, "16、24 或 32"},
		{"TLS on plaintext grpc", "grpc", map[string]string{"grpc_insecure": "true", "tls_verify": "true"}, "明文"},
		{"TLS missing private key", "tls", map[string]string{"tls_server_cert_file": "/etc/gost/server.crt"}, "成对"},
		{"TLS client CA without certificates", "tls", map[string]string{"tls_client_ca_file": "/etc/gost/client-ca.pem"}, "客户端证书"},
		{"TLS impossible versions", "tls", map[string]string{"tls_min_version": "VersionTLS13", "tls_max_version": "VersionTLS12"}, "最低"},
		{"WebSocket ALPN mismatch", "wss", map[string]string{"tls_alpn": "h2, http/1.1"}, "必须是 http/1.1"},
		{"HTTP/2 ALPN mismatch", "h2", map[string]string{"tls_alpn": "http/1.1"}, "必须是 h2"},
		{"PHTS ALPN mismatch", "phts", map[string]string{"tls_alpn": "relay"}, "仅支持 h2 和 http/1.1"},
		{"grpc authority mismatch", "grpc", map[string]string{"host": "host.example", "tls_server_name": "cert.example"}, "须一致"},
		{"conflicting pht routes", "pht", map[string]string{"push_path": "/%70ull"}, "各不相同"},
		{"ignored kcp interval", "kcp", map[string]string{"kcp_mode": "fast", "kcp_interval": "20"}, "manual"},
		{"invalid kcp FEC", "kcp", map[string]string{"kcp_data_shards": "0", "kcp_parity_shards": "3"}, "分片"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateRelayOptions(tc.protocol, tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func checkRelayJSON(t *testing.T, config any, want string) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected any
	if err = json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Gost configuration = %s, want %s", data, want)
	}
}

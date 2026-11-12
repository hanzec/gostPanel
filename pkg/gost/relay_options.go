package gost

import (
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed relay_options.json
var relayOptionSchema []byte

type relayField struct {
	ID       string
	Label    string
	Kind     string
	Listener string
	Dialer   string
	Min      int64
	Max      int64
	Choices  []string
}

var relayFields = loadRelayFields()

func loadRelayFields() map[string]map[string]relayField {
	var schema struct {
		Protocols map[string][]string
		Groups    map[string][]relayField
	}
	if err := json.Unmarshal(relayOptionSchema, &schema); err != nil {
		panic(err)
	}
	protocols := make(map[string]map[string]relayField, len(schema.Protocols))
	for protocol, groups := range schema.Protocols {
		fields := make(map[string]relayField)
		for _, group := range groups {
			groupFields, ok := schema.Groups[group]
			if !ok {
				panic("unknown Gost option group: " + group)
			}
			for _, field := range groupFields {
				if _, duplicate := fields[field.ID]; duplicate {
					panic("duplicate Gost option: " + protocol + "/" + field.ID)
				}
				fields[field.ID] = field
			}
		}
		protocols[protocol] = fields
	}
	return protocols
}

// ValidateRelayOptions checks the options accepted by the selected Gost transport.
func ValidateRelayOptions(protocol string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	fields := relayFields[protocol]
	for _, id := range slices.Sorted(maps.Keys(values)) {
		field, ok := fields[id]
		if !ok {
			return fmt.Errorf("%s 通道不支持参数 %q", strings.ToUpper(protocol), id)
		}
		if err := field.validate(values[id]); err != nil {
			return fmt.Errorf("%s：%w", field.Label, err)
		}
		if values["grpc_insecure"] == "true" && strings.HasPrefix(id, "tls_") {
			return fmt.Errorf("gRPC 明文模式不能配置 TLS 参数")
		}
	}
	return validateRelayOptionCombinations(protocol, values)
}

var (
	headerName      = regexp.MustCompile("^[!#$%&'*+.^_\x60|~0-9A-Za-z-]+$")
	grpcServiceName = regexp.MustCompile(`^/?[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*(/[A-Za-z_][A-Za-z0-9_]*)?$`)
)

func (f relayField) validate(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 4096 || strings.ContainsRune(value, 0) {
		return fmt.Errorf("请填写非空值，长度不得超过 4096 字节")
	}
	if f.Kind != "headers" && strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("请填写单行值")
	}
	switch f.Kind {
	case "boolean":
		if value != "true" && value != "false" {
			return fmt.Errorf("请选择启用或禁用")
		}
	case "integer":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < f.Min || n > f.Max {
			return fmt.Errorf("请输入 %d 到 %d 的整数", f.Min, f.Max)
		}
	case "duration":
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 0 {
			return fmt.Errorf("请输入非负时长，例如 30s 或 2m")
		}
	case "select":
		if !slices.Contains(f.Choices, value) {
			return fmt.Errorf("请选择受支持的值")
		}
	case "path":
		u, err := url.ParseRequestURI(value)
		if err != nil || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") ||
			u.ForceQuery || u.RawQuery != "" || strings.ContainsAny(u.Path, "{}?# \t\r\n\x00") ||
			(u.Path != "/" && path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/")) {
			return fmt.Errorf("请输入以 / 开头的路径，且不带查询参数")
		}
	case "file":
		if !path.IsAbs(value) || value == "/" || path.Clean(value) != value {
			return fmt.Errorf("请输入节点上文件的绝对路径")
		}
	case "headers":
		var headers map[string]any
		if err := json.Unmarshal([]byte(value), &headers); err != nil || headers == nil {
			return fmt.Errorf("请输入 JSON 对象，且每个请求头或响应头的值均为字符串")
		}
		for name, raw := range headers {
			header, ok := raw.(string)
			if !ok || !headerName.MatchString(name) || strings.ContainsAny(header, "\r\n\x00") {
				return fmt.Errorf("HTTP 头名称或值无效")
			}
			switch strings.ToLower(name) {
			case "host", "content-length", "transfer-encoding", "connection", "upgrade", "trailer":
				return fmt.Errorf("该 HTTP 头由通道处理")
			}
			if strings.HasPrefix(strings.ToLower(name), "sec-websocket-") {
				return fmt.Errorf("WebSocket 握手头由通道处理")
			}
		}
	case "csv":
		for _, item := range relayCSV(value) {
			if item == "" || len(item) > 255 {
				return fmt.Errorf("请用逗号分隔非空值")
			}
			if f.ID == "tls_cipher_suites" && !isTLS12CipherSuite(item) {
				return fmt.Errorf("请选择 TLS 1.2 密码套件")
			}
		}
	case "text":
		if f.ID == "grpc_service" && !grpcServiceName.MatchString(value) {
			return fmt.Errorf("请输入有效的 gRPC 服务名或方法路径，例如 /GostTunel/Tunnel")
		}
		if f.ID == "quic_cipher_key" && len(value) != 16 && len(value) != 24 && len(value) != 32 {
			return fmt.Errorf("AES 密钥长度必须是 16、24 或 32 字节")
		}
	}
	return nil
}

func validateRelayOptionCombinations(protocol string, values map[string]string) error {
	serverCert := values["tls_server_cert_file"] != ""
	clientCert := values["tls_client_cert_file"] != ""
	if serverCert != (values["tls_server_key_file"] != "") || clientCert != (values["tls_client_key_file"] != "") {
		return fmt.Errorf("TLS 证书和私钥必须成对配置")
	}
	if values["tls_client_ca_file"] != "" && (!serverCert || !clientCert) {
		return fmt.Errorf("校验客户端证书需要同时配置入口和出口的证书及私钥")
	}
	if values["tls_min_version"] == "VersionTLS13" && values["tls_max_version"] == "VersionTLS12" {
		return fmt.Errorf("最低 TLS 版本不能高于最高版本")
	}
	switch protocol {
	case "wss", "mwss":
		if alpn := values["tls_alpn"]; alpn != "" && strings.TrimSpace(alpn) != "http/1.1" {
			return fmt.Errorf("WebSocket 的 TLS ALPN 必须是 http/1.1")
		}
	case "h2":
		if alpn := values["tls_alpn"]; alpn != "" && strings.TrimSpace(alpn) != "h2" {
			return fmt.Errorf("HTTP/2 的 TLS ALPN 必须是 h2")
		}
	case "grpc":
		if authority, name := grpcAuthority(values["host"]), values["tls_server_name"]; authority != "" && name != "" && authority != name {
			return fmt.Errorf("gRPC Authority 和 TLS 服务端名称必须一致")
		}
	case "pht", "phts", "h3":
		if alpn := values["tls_alpn"]; protocol == "phts" && alpn != "" {
			for _, name := range relayCSV(alpn) {
				if name != "h2" && name != "http/1.1" {
					return fmt.Errorf("PHTS 的 TLS ALPN 仅支持 h2 和 http/1.1")
				}
			}
		}
		authorize := phtPath(values["authorize_path"], "/authorize")
		push := phtPath(values["push_path"], "/push")
		pull := phtPath(values["pull_path"], "/pull")
		if authorize == push || authorize == pull || push == pull {
			return fmt.Errorf("授权、推送和拉取路径必须各不相同")
		}
	case "kcp":
		if mode := values["kcp_mode"]; mode != "" && mode != "manual" {
			for _, id := range []string{"kcp_interval", "kcp_no_delay", "kcp_resend", "kcp_no_congestion"} {
				if values[id] != "" {
					return fmt.Errorf("自定义 KCP 更新、重传及拥塞控制参数需要选择 manual 模式")
				}
			}
		}
		data, parity := 10, 3
		if values["kcp_data_shards"] != "" {
			data, _ = strconv.Atoi(values["kcp_data_shards"])
		}
		if values["kcp_parity_shards"] != "" {
			parity, _ = strconv.Atoi(values["kcp_parity_shards"])
		}
		if (data == 0) != (parity == 0) || data+parity > 255 {
			return fmt.Errorf("KCP FEC 分片须同时为 0，或同时为正且合计不超过 255")
		}
	}
	return nil
}

func grpcAuthority(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil && name != "" {
		return name
	}
	return host
}

func phtPath(value, fallback string) string {
	if value != "" {
		// ServeMux compares decoded patterns, so /%70ull conflicts with /pull.
		pattern, _ := url.PathUnescape(value)
		return pattern
	}
	return fallback
}

func isTLS12CipherSuite(name string) bool {
	for _, suite := range tls.CipherSuites() {
		if name == suite.Name && slices.Contains(suite.SupportedVersions, tls.VersionTLS12) {
			return true
		}
	}
	return false
}

func relayCSV(value string) []string {
	items := strings.Split(value, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return items
}

// RelayListener builds the exit listener from previously validated options.
func RelayListener(protocol string, values map[string]string) *ListenerConfig {
	metadata, tlsConfig := relayTransportConfig(protocol, values, false)
	return &ListenerConfig{Type: protocol, Metadata: metadata, TLS: tlsConfig}
}

// RelayDialer builds the entry dialer from previously validated options.
func RelayDialer(protocol string, values map[string]string) *DialerConfig {
	metadata, tlsConfig := relayTransportConfig(protocol, values, true)
	return &DialerConfig{Type: protocol, Metadata: metadata, TLS: tlsConfig}
}

func relayTransportConfig(protocol string, values map[string]string, dialer bool) (map[string]any, map[string]any) {
	if len(values) == 0 {
		return nil, nil
	}
	metadata := make(map[string]any)
	tlsConfig := make(map[string]any)
	tlsOptions := make(map[string]any)
	if protocol == "kcp" {
		// Gost starts a new KCP config from zero when kcp.config is present.
		// Retain the defaults that its individual metadata fields don't set.
		metadata["kcp.config"] = map[string]any{"datashard": 10, "parityshard": 3, "sockbuf": 4194304, "snmpperiod": 60}
	}
	for id, value := range values {
		field := relayFields[protocol][id]
		key := field.Listener
		if dialer {
			key = field.Dialer
		}
		switch {
		case key == "":
		case strings.HasPrefix(key, "tls:options."):
			tlsOptions[strings.TrimPrefix(key, "tls:options.")] = field.configValue(value)
		case strings.HasPrefix(key, "tls:"):
			tlsConfig[strings.TrimPrefix(key, "tls:")] = field.configValue(value)
		case strings.HasPrefix(key, "kcp:"):
			nested := metadata["kcp.config"].(map[string]any)
			nested[strings.TrimPrefix(key, "kcp:")] = field.configValue(value)
		default:
			// Gost's metadata integer getter accepts JSON strings, but not
			// numbers decoded as float64. Leave scalar metadata in text form.
			if field.Kind == "headers" {
				metadata[key] = field.configValue(value)
			} else {
				metadata[key] = value
			}
		}
	}
	if len(tlsOptions) > 0 {
		tlsConfig["options"] = tlsOptions
	}
	if dialer && protocol == "grpc" && values["grpc_insecure"] != "true" {
		// gRPC requires the authority and the TLS transport credentials to agree.
		if authority := grpcAuthority(values["host"]); authority != "" {
			tlsConfig["serverName"] = authority
		} else if name := values["tls_server_name"]; name != "" {
			metadata["grpc.authority"] = name
		}
	}
	return metadata, tlsConfig
}

func (f relayField) configValue(value string) any {
	switch f.Kind {
	case "boolean":
		return value == "true"
	case "integer":
		number, _ := strconv.Atoi(value)
		return number
	case "csv":
		return relayCSV(value)
	case "headers":
		var headers map[string]string
		_ = json.Unmarshal([]byte(value), &headers)
		return headers
	default:
		return value
	}
}

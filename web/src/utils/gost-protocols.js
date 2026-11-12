import relayOptions from '../../../pkg/gost/relay_options.json'

export const tunnelProtocols = Object.keys(relayOptions.protocols)

const groupTitles = {
  tcp_keepalive: 'TCP 保活', ws_tcp_keepalive: 'TCP 保活', grpc_tcp_keepalive: 'TCP 保活',
  mptcp: '连接与监听', reuse_port: '连接与监听', backlog: '连接与监听', tcp_handshake: '连接与监听',
  mux: '多路复用', ws: 'WebSocket', h2: 'HTTP/2', grpc: 'gRPC', pht: 'HTTP 隧道', h3: 'HTTP/3',
  udp: 'UDP 会话', quic_timing: 'QUIC', quic: 'QUIC', h3_keepalive: 'QUIC', kcp: 'KCP',
  ohttp: 'HTTP 混淆', otls: 'TLS 混淆', tls: 'TLS 证书与握手', tls_versions: 'TLS 证书与握手',
  tls_alpn: 'TLS 证书与握手', tls_ws_alpn: 'TLS 证书与握手', tls_h2_alpn: 'TLS 证书与握手'
}

export const protocolOptionGroups = protocol => {
  const groups = new Map()
  for (const group of relayOptions.protocols[protocol] || []) {
    const title = groupTitles[group] || group
    groups.set(title, [...(groups.get(title) || []), ...(relayOptions.groups[group] || [])])
  }
  return [...groups].map(([title, fields]) => ({ title, fields }))
}

export const protocolOptionFields = protocol => {
  const fields = (relayOptions.protocols[protocol] || []).flatMap(group => relayOptions.groups[group] || [])
  return [...new Map(fields.map(field => [field.id, field])).values()]
}

export const supportedProtocolOptions = (options, mptcpSupported, savedMptcp) => {
  const supported = { ...options }
  // Older daemons may not report support; keep an unchanged value saved for this exit.
  if (mptcpSupported !== true && options.mptcp !== savedMptcp) delete supported.mptcp
  return supported
}

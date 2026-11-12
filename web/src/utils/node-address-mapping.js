export const formatEndpoint = (ip, port) => `${ip?.includes(':') ? `[${ip}]` : ip || '*'}:${port}`

export const mappedConnectEndpoint = (node, listenIP, port) => {
  const connectIP = node?.address_mappings?.find(mapping => mapping.listen_ip === listenIP)?.connect_ip
  return connectIP && port ? formatEndpoint(connectIP, port) : ''
}

import { execFileSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { setTimeout as delay } from 'node:timers/promises'

const mode = process.argv[2]
const upgrade = mode === 'depoly'
const path = upgrade ? '/api/deploy/deploy-tool' : '/api/deploy/panel'
const push = process.env.GITHUB_EVENT_NAME === 'push'
const {
  WEBHOOK_URL: configuredURL,
  WEBHOOK_SECRET: secret,
  WEBHOOK_TOKEN: token,
  WEBHOOK_PAYLOAD: payload,
} = process.env
delete process.env.WEBHOOK_SECRET
delete process.env.WEBHOOK_TOKEN

async function main() {
  if (mode !== 'compose' && !upgrade) throw new Error('Invalid webhook mode.')
  if (!isCurrentPush()) return
  if (upgrade && push && !(configuredURL && secret && token)) {
    console.log('Skipping automatic tool upgrade until the production-tool URL and credentials are configured.')
    return
  }

  const origin = /^https:\/\/[A-Za-z0-9.-]+/.exec(configuredURL)?.[0]
  if (!origin || ![origin + '/api/deploy', origin + '/api/deploy/', origin + path].includes(configuredURL)) {
    throw new Error(`Webhook URL must use HTTPS and end in /api/deploy or ${path}.`)
  }
  if (!/^[0-9a-f]{64}$/.test(secret) || !/^[0-9a-f]{64}$/.test(token)) {
    throw new Error('Webhook signing secret and token must be 64 lowercase hexadecimal characters.')
  }
  if (upgrade ? payload !== 'upgrade' : !/^sha256:[0-9a-f]{64}$/.test(payload)) {
    throw new Error('Missing or invalid webhook payload.')
  }

  if (upgrade && push) {
    console.log('Waiting 60 seconds for the latest release cache to expire.')
    await delay(60_000)
  }
  for (let attempt = 1; attempt <= 3; attempt++) {
    if (!isCurrentPush()) return
    console.log(`Sending ${path} webhook (attempt ${attempt} of 3).`)
    const response = await sendWebhook(origin)
    if (response.status === 202) {
      await waitForUpdate(origin, await acceptedJob(response))
      return
    }
    const retryAfter = response.headers.get('retry-after')
    await response.body?.cancel()
    if (response.status === 429 && attempt < 3 && /^[1-9][0-9]{0,2}$/.test(retryAfter) && Number(retryAfter) <= 300) {
      console.log(`Webhook returned HTTP 429. Retrying in ${retryAfter} seconds.`)
      await delay(Number(retryAfter) * 1000)
      continue
    }
    if (response.status === 404) {
      console.error('Compare the webhook host with the installer output and check the active public Traefik route for /api/deploy/.')
      console.error('Check the receiver with journalctl --namespace=+yagp-depoly -u yagp-depoly.service.')
      if (upgrade) console.error('Check that setup_deploy.sh install-upgrade enabled the tool endpoint.')
      throw new Error(`Webhook ${path} returned HTTP 404.`)
    }
    throw new Error(`Webhook rejected the request (HTTP ${response.status} after ${attempt} attempt(s)).`)
  }
}

function isCurrentPush() {
  if (!push) return true
  const remote = `https://github.com/${process.env.GITHUB_REPOSITORY}.git`
  const result = execFileSync('git', ['ls-remote', '--exit-code', remote, 'refs/heads/master'], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], timeout: 15_000,
  })
  if (result.split('\t')[0] === process.env.GITHUB_SHA) return true
  console.log('Skipping webhook request because master has advanced.')
  return false
}

async function sendWebhook(origin) {
  const timestamp = Math.floor(Date.now() / 1000).toString()
  const signature = createHmac('sha256', secret).update(timestamp + '\n' + payload).digest('hex')
  try {
    return await fetch(origin + path, {
      method: 'POST',
      redirect: 'manual',
      signal: AbortSignal.timeout(30_000),
      headers: {
        Authorization: 'Bearer ' + token,
        'Content-Type': 'text/plain',
        'X-Yagp-Timestamp': timestamp,
        'X-Yagp-Signature': 'sha256=' + signature,
      },
      body: payload,
    })
  } catch {
    throw new Error(`Could not send the ${path} webhook over HTTPS.`)
  }
}

async function acceptedJob(response) {
  const body = await response.text()
  if (!body.trim()) {
    throw new Error('The webhook returned no job status URL. Migrate the server to the split yagp-depoly and yagp-update tools before retrying.')
  }
  const job = parseJSON(body)
  const statusPattern = new RegExp(`^/api/deploy/status/[0-9a-f]{32}\\?expires=[0-9]+&mode=${mode}&signature=[0-9a-f]{64}$`)
  if (typeof job?.status_url !== 'string' || !statusPattern.test(job.status_url)) {
    throw new Error('Invalid update status URL.')
  }
  return job
}

async function waitForUpdate(origin, job) {
  const signal = AbortSignal.timeout(960_000)
  let previousState = ''
  let previousLogs = ''
  const report = (status) => {
    if (status?.mode !== mode || !['queued', 'running', 'succeeded', 'failed'].includes(status.state)) {
      throw new Error('Invalid update job status.')
    }
    if (status.state !== previousState) {
      console.log(`Update job is ${status.state}.`)
      previousState = status.state
    }
    const logs = typeof status.logs === 'string' ? status.logs : ''
    const newLogs = logs.startsWith(previousLogs) ? logs.slice(previousLogs.length) : logs
    for (const line of newLogs.split('\n')) {
      if (line) console.log('server | ' + line.replace(/[\x00-\x1f\x7f]/g, ''))
    }
    previousLogs = logs
    if (status.state === 'failed') {
      const result = Number.isInteger(status.exit_code) ? ` with exit code ${status.exit_code}` : ''
      throw new Error(`Update job failed${result}. See the update logs above and the yagp-depoly journal on the server.`)
    }
    if (status.state === 'succeeded') {
      if (status.exit_code !== 0) throw new Error('Update did not exit successfully.')
      return true
    }
    return false
  }

  console.log('Webhook accepted. Waiting for the update job to finish.')
  const url = origin + job.status_url
  if (job.status_websocket !== true) {
    console.log('This receiver uses HTTPS job status. A newer receiver will stream updates over WebSocket.')
  }
  while (!signal.aborted) {
    const done = job.status_websocket === true
      ? await watchStatus(url.replace(/^https:/, 'wss:'), signal, report)
      : await pollStatus(url, signal, report)
    if (done) return
    await delay(4_000, undefined, { signal }).catch(() => {})
  }
  throw new Error('Timed out waiting for the update job.')
}

function watchStatus(url, signal, report) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url)
    let finished = false
    const connectTimer = setTimeout(() => finish(false), 15_000)
    const stop = () => finish(false)
    function finish(done, error) {
      if (finished) return
      finished = true
      clearTimeout(connectTimer)
      signal.removeEventListener('abort', stop)
      socket.close()
      if (error) reject(error)
      else resolve(done)
    }
    signal.addEventListener('abort', stop, { once: true })
    socket.addEventListener('open', () => clearTimeout(connectTimer), { once: true })
    socket.addEventListener('close', stop, { once: true })
    socket.addEventListener('error', stop, { once: true })
    socket.addEventListener('message', ({ data }) => {
      if (finished) return
      try {
        if (report(parseJSON(data))) finish(true)
      } catch (error) {
        finish(false, error)
      }
    })
  })
}

async function pollStatus(url, signal, report) {
  let body
  try {
    const response = await fetch(url, {
      redirect: 'error',
      signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
    })
    if (response.status !== 200) {
      await response.body?.cancel()
      return false
    }
    body = await response.text()
  } catch {
    return false
  }
  return report(parseJSON(body))
}

function parseJSON(text) {
  try {
    return JSON.parse(text)
  } catch {
    throw new Error('Invalid JSON from the deployment webhook.')
  }
}

try {
  await main()
} catch (error) {
  console.error(error.message)
  process.exitCode = 1
}

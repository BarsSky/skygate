// mobile-overflow-check.cjs — measure the reworked pages at a REAL phone viewport.
//
// Usage: node mobile-overflow-check.cjs <dir-with-html> [width] [--shot out.png]
//
// It launches headless Edge/Chrome once with the DevTools protocol, opens every
// *.html in the directory, emulates a 390x844 mobile viewport (the width the
// project's mobile audit targets), and reports the document's horizontal overflow
// plus the widest offending element when there is one. Exit code 1 on overflow.
//
// The exit-code contract is deliberately two-sided: 1 means "a measurement ran and
// the page overflows" (a product claim), 0 with a `SKIP: …` line means "nothing was
// measured" (no browser, or a browser that started and never published a DevTools
// port — a loaded or restricted runner, not a defect). A caller must never turn the
// second into the first.
//
// --shot writes a PNG of the LAST page at the emulated width, because "it does not
// overflow" and "it is readable" are two different questions and only a picture
// answers the second.
//
// This is the one check the static guards cannot do: they assert the SHAPE of the
// markup (table inside .table-wrap, no inline width over a budget), while this asks
// a browser what the shape actually does at 390px.
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const argv = process.argv.slice(2)
const shotIdx = argv.indexOf('--shot')
const shotPath = shotIdx >= 0 ? argv[shotIdx + 1] : null
if (shotIdx >= 0) argv.splice(shotIdx, 2)
const dir = argv[0]
const width = Number(argv[1] || 390)
const height = 844
if (!dir) {
  console.error('usage: node mobile-overflow-check.cjs <dir> [width] [--shot out.png]')
  process.exit(2)
}

const candidates = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  '/usr/bin/chromium', '/usr/bin/chromium-browser', '/usr/bin/google-chrome',
]
const bin = candidates.find((p) => fs.existsSync(p))
if (!bin) {
  console.log('SKIP: no headless browser found (Edge/Chrome/chromium)')
  process.exit(0)
}

// Port 0 lets the browser pick a free one and publish it in DevToolsActivePort.
// A hardcoded port is a flake waiting to happen: a browser killed mid-run (or a
// second run overlapping the first) still holds it, and the driver then talks to
// a STRANGER's tab — measuring a page it never opened.
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'mobcdp-'))
// stderr is captured, not discarded: when a browser starts and then refuses to
// publish a port (small /dev/shm on a CI runner, a wrapper script that exits, a
// missing shared library), the browser's own last words are the only diagnosis
// there is, and a check that says "cannot measure" must be able to say why.
let browserErr = ''
let browserExit = ''
const proc = spawn(bin, [
  '--headless=new', '--disable-gpu', '--no-sandbox', '--hide-scrollbars',
  '--disable-dev-shm-usage', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--remote-debugging-port=0', '--user-data-dir=' + profile,
  'about:blank',
], { stdio: ['ignore', 'ignore', 'pipe'] })
proc.stderr.on('data', (d) => {
  if (browserErr.length < 4000) browserErr += d.toString()
})
proc.on('exit', (code, sig) => {
  browserExit = `exited code=${code}${sig ? ' signal=' + sig : ''}`
})
proc.on('error', (e) => {
  browserExit = `could not be launched: ${e.message}`
})

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let port = 0
async function discoverPort() {
  const file = path.join(profile, 'DevToolsActivePort')
  // 200 x 150 ms = 30 s: a cold first launch on a loaded CI runner is a few
  // seconds, and the old 15 s budget turned one slow start into a FAIL.
  let giveUpAt = 0
  for (let i = 0; i < 200; i++) {
    try {
      const txt = fs.readFileSync(file, 'utf8').split('\n')
      const p = Number(txt[0])
      if (p > 0) return p
    } catch {}
    if (browserExit) {
      // A launcher that exited and published nothing is a dead end: waiting the
      // full 30 s would only delay the diagnosis. A launcher that re-execs the
      // real browser leaves a short grace period for the child to publish.
      if (!giveUpAt) giveUpAt = i + 4
      if (i >= giveUpAt) return 0
    }
    await sleep(150)
  }
  return 0
}

async function newTab(url) {
  const r = await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' })
  return r.json()
}

function connect(wsUrl) {
  const ws = new WebSocket(wsUrl)
  let id = 0
  const pending = new Map()
  const ready = new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data)
    if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id) }
  }
  const send = (method, params) => new Promise((res) => {
    const myId = ++id
    pending.set(myId, res)
    ws.send(JSON.stringify({ id: myId, method, params: params || {} }))
  })
  return { ready, send, close: () => ws.close() }
}

;(async () => {
  port = await discoverPort()
  if (!port) {
    // "I could not measure" is NOT "the page overflows". Exit 0 with a SKIP line
    // that carries the reason, so the calling check reports SKIP (AGENTS rule 1)
    // and a runner where the browser cannot start never invents a product defect.
    const why = browserErr
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .slice(-3)
      .join(' | ')
      console.log(
        'SKIP: the browser started but never published a DevTools port' +
          (browserExit ? ` (it ${browserExit})` : '') +
          (why ? `: ${why.slice(0, 400)}` : ''),
      )
    proc.kill()
    try { fs.rmSync(profile, { recursive: true, force: true }) } catch {}
    process.exit(0)
  }
  const files = fs.readdirSync(dir).filter((f) => f.endsWith('.html')).sort()
  let failed = 0
  const pages = []
  for (const f of files) {
    const url = 'file:///' + path.join(dir, f).replace(/\\/g, '/')
    const tab = await newTab(url)
    const c = connect(tab.webSocketDebuggerUrl)
    await c.ready
    await c.send('Emulation.setDeviceMetricsOverride', {
      width, height, deviceScaleFactor: 2, mobile: true,
    })
    await c.send('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 })
    await c.send('Runtime.enable')
    await c.send('Page.enable')
    await sleep(500)
    const expr = `(() => {
      const d = document.documentElement;
      const over = d.scrollWidth - d.clientWidth;
      let worst = null;
      if (over > 0) {
        const vw = d.clientWidth;
        for (const el of document.querySelectorAll('body *')) {
          const r = el.getBoundingClientRect();
          if (r.right > vw + 1) {
            const w = Math.round(r.width);
            if (!worst || w > worst.w) worst = { w, tag: el.tagName.toLowerCase(), cls: (el.className||'').toString().slice(0,40) };
          }
        }
      }
      return JSON.stringify({ over, sw: d.scrollWidth, cw: d.clientWidth, docH: Math.round(document.body.scrollHeight), worst });
    })()`
    const res = await c.send('Runtime.evaluate', { expression: expr, returnByValue: true })
    const val = JSON.parse(res.result.result.value)
    if (val.over > 0) {
      failed++
      console.log(`FAIL ${f}: horizontal overflow ${val.over}px (scrollWidth ${val.sw} vs ${val.cw})${val.worst ? `; widest offender <${val.worst.tag} class="${val.worst.cls}"> ${val.worst.w}px` : ''}`)
    } else {
      console.log(`PASS ${f}: no horizontal overflow at ${width}px (content ${val.cw}px, page ${val.docH}px tall)`)
      pages.push({ f, docH: val.docH })
    }
    if (shotPath && f === files[files.length - 1]) {
      const shot = await c.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true })
      fs.writeFileSync(shotPath, Buffer.from(shot.result.data, 'base64'))
      console.log(`SHOT ${shotPath}: ${f} at ${width}px (full page ${val.docH}px)`)
    }
    c.close()
    await fetch(`http://127.0.0.1:${port}/json/close/${tab.id}`)
  }
  proc.kill()
  try { fs.rmSync(profile, { recursive: true, force: true }) } catch {}
  process.exit(failed === 0 ? 0 : 1)
})().catch((e) => { console.error('ERROR', e); proc.kill(); process.exit(2) })

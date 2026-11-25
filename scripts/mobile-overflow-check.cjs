// mobile-overflow-check.cjs — measure the reworked pages at a REAL phone viewport.
//
// Usage: node mobile-overflow-check.cjs <dir-with-html> [width]
//
// It launches headless Edge/Chrome once with the DevTools protocol, opens every
// *.html in the directory, emulates a 390x844 mobile viewport (the width the
// project's mobile audit targets), and reports the document's horizontal overflow
// plus the widest offending element when there is one. Exit code 1 on overflow.
//
// This is the one check the static guards cannot do: they assert the SHAPE of the
// markup (table inside .table-wrap, no inline width over a budget), while this asks
// a browser what the shape actually does at 390px.
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')

const dir = process.argv[2]
const width = Number(process.argv[3] || 390)
const height = 844
if (!dir) {
  console.error('usage: node mobile-overflow-check.cjs <dir> [width]')
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

const port = 9333
const proc = spawn(bin, [
  '--headless=new', '--disable-gpu', '--no-sandbox', '--hide-scrollbars',
  `--remote-debugging-port=${port}`, '--user-data-dir=' + path.join(require('node:os').tmpdir(), 'mobcdp-' + process.pid),
  'about:blank',
], { stdio: 'ignore' })

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

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
  // wait for the DevTools endpoint
  for (let i = 0; i < 60; i++) {
    try { const r = await fetch(`http://127.0.0.1:${port}/json/version`); if (r.ok) break } catch {}
    await sleep(250)
  }
  const files = fs.readdirSync(dir).filter((f) => f.endsWith('.html')).sort()
  let failed = 0
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
      return JSON.stringify({ over, sw: d.scrollWidth, cw: d.clientWidth, worst });
    })()`
    const res = await c.send('Runtime.evaluate', { expression: expr, returnByValue: true })
    const val = JSON.parse(res.result.result.value)
    if (val.over > 0) {
      failed++
      console.log(`FAIL ${f}: horizontal overflow ${val.over}px (scrollWidth ${val.sw} vs ${val.cw})${val.worst ? `; widest offender <${val.worst.tag} class="${val.worst.cls}"> ${val.worst.w}px` : ''}`)
    } else {
      console.log(`PASS ${f}: no horizontal overflow at ${width}px (content ${val.cw}px)`)
    }
    c.close()
    await fetch(`http://127.0.0.1:${port}/json/close/${tab.id}`)
  }
  proc.kill()
  process.exit(failed === 0 ? 0 : 1)
})().catch((e) => { console.error('ERROR', e); proc.kill(); process.exit(2) })

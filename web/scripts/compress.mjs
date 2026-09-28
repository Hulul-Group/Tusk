// Post-build: precompress text assets in dist/ as .gz and .br so internal/webui
// can serve them without compressing per request. Only files > 1 KiB whose
// compressed form is actually smaller are kept.
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { brotliCompressSync, constants, gzipSync } from "node:zlib"

const root = new URL("../dist/", import.meta.url).pathname
const exts = /\.(js|mjs|css|html|svg|json|txt|xml|webmanifest|ico|map)$/i
let count = 0
let raw = 0
let gz = 0
let br = 0

function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      walk(p)
      continue
    }
    if (!exts.test(name)) continue
    const buf = readFileSync(p)
    if (buf.length < 1024) continue
    const g = gzipSync(buf, { level: 9 })
    const b = brotliCompressSync(buf, {
      params: {
        [constants.BROTLI_PARAM_QUALITY]: 11,
        [constants.BROTLI_PARAM_SIZE_HINT]: buf.length,
      },
    })
    if (g.length < buf.length) writeFileSync(p + ".gz", g)
    if (b.length < buf.length) writeFileSync(p + ".br", b)
    count++
    raw += buf.length
    gz += g.length
    br += b.length
  }
}

walk(root)
const kb = (n) => (n / 1024).toFixed(1) + " kB"
console.log(`precompressed ${count} files: raw ${kb(raw)} → gzip ${kb(gz)} / brotli ${kb(br)}`)

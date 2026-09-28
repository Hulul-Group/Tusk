// k6 load test for the Tusk redirect hot path.
//
//   k6 run -e BASE=https://s.example.com -e IDS=abc123,def456 deploy/k6/redirect.js
//   k6 run -e BASE=http://127.0.0.1:3001 -e IDS=abc123 -e RATE=20000 -e DURATION=2m deploy/k6/redirect.js
//
// Env:
//   BASE      origin or public URL (default http://127.0.0.1:3000)
//   IDS       comma-separated existing short-link addresses (hot set); required
//   MISS_PCT  percentage of requests to random non-existent ids (default 5) — exercises the negative cache
//   RATE      target requests/second for the constant-arrival scenario (default 2000)
//   DURATION  (default 1m)
//   HOST      optional Host header override (test a custom domain against an origin IP)
//
// Thresholds encode the architecture budget (docs/ARCHITECTURE.md §2): p99 < 5 ms at the
// origin on cache hits. Through Cloudflare, raise P99_MS (e.g. -e P99_MS=80).
import http from "k6/http"
import { check } from "k6"
import { Counter, Trend } from "k6/metrics"

const BASE = (__ENV.BASE || "http://127.0.0.1:3000").replace(/\/$/, "")
const IDS = (__ENV.IDS || "").split(",").map((s) => s.trim()).filter(Boolean)
const MISS_PCT = Number(__ENV.MISS_PCT || 5)
const RATE = Number(__ENV.RATE || 2000)
const DURATION = __ENV.DURATION || "1m"
const P99_MS = Number(__ENV.P99_MS || 5)
const HOST = __ENV.HOST

if (IDS.length === 0) {
  throw new Error("set IDS=<comma-separated existing short ids>")
}

const hitLatency = new Trend("redirect_hit_ms", true)
const missLatency = new Trend("redirect_miss_ms", true)
const unexpected = new Counter("unexpected_status")

export const options = {
  discardResponseBodies: true,
  scenarios: {
    redirects: {
      executor: "constant-arrival-rate",
      rate: RATE,
      timeUnit: "1s",
      duration: DURATION,
      preAllocatedVUs: Math.max(50, Math.ceil(RATE / 50)),
      maxVUs: Math.max(200, Math.ceil(RATE / 10)),
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.001"],
    redirect_hit_ms: [`p(99)<${P99_MS}`],
    unexpected_status: ["count<1"],
  },
}

const params = {
  redirects: 0, // measure the 302 itself, not the target site
  headers: Object.assign(
    { "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 k6" },
    HOST ? { Host: HOST } : {},
  ),
  tags: { name: "redirect" },
}

// Zipf-ish: a few very hot links, a long tail (realistic for shorteners).
function pickId() {
  const r = Math.random()
  const idx = Math.floor(Math.pow(r, 3) * IDS.length)
  return IDS[Math.min(idx, IDS.length - 1)]
}

function randomId() {
  const a = "abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789"
  let s = "zz"
  for (let i = 0; i < 8; i++) s += a[Math.floor(Math.random() * a.length)]
  return s
}

export default function () {
  const miss = Math.random() * 100 < MISS_PCT
  const id = miss ? randomId() : pickId()
  const res = http.get(`${BASE}/${id}`, Object.assign({}, params, { responseCallback: http.expectedStatuses(200, 302, 404) }))
  if (miss) {
    missLatency.add(res.timings.duration)
    if (!check(res, { "miss → 404": (r) => r.status === 404 })) unexpected.add(1)
  } else {
    hitLatency.add(res.timings.duration)
    // 302 for normal links; 200 for password-protected (form page).
    if (!check(res, { "hit → 302/200": (r) => r.status === 302 || r.status === 200 })) unexpected.add(1)
  }
}

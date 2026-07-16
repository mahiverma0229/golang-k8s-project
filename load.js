// ─────────────────────────────────────────────────────────────────────────────
// k6 Load Test — AI Incident Response Platform
//
// Purpose:
//   Simulate 100 concurrent users submitting incident analysis requests to
//   the /analyze endpoint.  This lets us observe:
//     - API response time under load
//     - Throughput (requests per second)
//     - Error rate
//     - Pod CPU / memory usage in Grafana (via Prometheus metrics)
//     - Kubernetes HPA scaling behaviour
//
// Run:
//   k6 run load.js
// ─────────────────────────────────────────────────────────────────────────────

import http from "k6/http";
import { check, sleep } from "k6";

// ── Load test configuration ──────────────────────────────────────────────────
// 100 virtual users (VUs) each continuously hitting the endpoint for 30 seconds.
// This mirrors realistic concurrent incident spikes.
export const options = {
  vus: 100,
  duration: '30s',
};

// ── Target base URL ───────────────────────────────────────────────────────────
const BASE_URL = 'https://app.incident-response.com';

// ── Sample incident payload ───────────────────────────────────────────────────
// Simulates an engineer submitting a crashing pod for analysis.
// In a real test you would rotate pod names across namespaces.
const PAYLOAD = JSON.stringify({
  pod_name:  "payment-service-abc12",
  namespace: "production"
});

const HEADERS = { "Content-Type": "application/json" };

// ── Default function — executed by every VU on every iteration ────────────────
export default function () {

  // POST /analyze — triggers K8s data collection + OpenAI call
  const res = http.post(`${BASE_URL}/analyze`, PAYLOAD, { headers: HEADERS });

  // Validate that the response indicates a successful analysis
  check(res, {
    'status is 200': (r) => r.status === 200,
    'response has analysis field': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.analysis !== undefined;
      } catch (_) {
        return false;
      }
    }
  });

  if (res.status !== 200) {
    console.error(`FAIL — status: ${res.status} body: ${res.body}`);
  }

  // Brief pause between requests — prevents overwhelming the LLM rate limit
  sleep(1);
}

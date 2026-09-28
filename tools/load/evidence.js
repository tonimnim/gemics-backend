// Run only against an isolated staging stack. This writes real storage objects.
// k6 run -e API_URL=https://staging.example -e TOKENS_FILE=./tokens.json 
//   -e IMAGE_FILE=./screenshot.jpg -e MEDIA_TYPE=image/jpeg -e RATE=10 -e DURATION=60s tools/load/evidence.js
import http from 'k6/http';
import crypto from 'k6/crypto';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { SharedArray } from 'k6/data';
import { Rate, Trend } from 'k6/metrics';

const api = (__ENV.API_URL || '').replace(/\/$/, '');
if (!api || !__ENV.TOKENS_FILE || !__ENV.IMAGE_FILE) throw new Error('API_URL, TOKENS_FILE and IMAGE_FILE are required');
const tokens = new SharedArray('test player tokens', () => JSON.parse(open(__ENV.TOKENS_FILE)));
const body = open(__ENV.IMAGE_FILE, 'b');
const digest = crypto.sha256(body, 'hex');
const success = new Rate('evidence_ready');
const readyTime = new Trend('evidence_ready_ms', true);
export const options = {
  // Never collect the url system tag: object URLs carry temporary credentials.
  systemTags: ['method', 'name', 'status', 'scenario', 'expected_response'],
  scenarios: { screenshots: {
    executor: 'constant-arrival-rate', rate: Number(__ENV.RATE || 1), timeUnit: '1s',
    duration: __ENV.DURATION || '10s', preAllocatedVUs: Number(__ENV.PREALLOCATED_VUS || 20),
    maxVUs: Number(__ENV.MAX_VUS || 1000), gracefulStop: '150s',
  } },
  thresholds: { evidence_ready: ['rate>0.99'], evidence_ready_ms: ['p(95)<60000'], dropped_iterations: ['count==0'] },
};

export default function () {
  const index = exec.scenario.iterationInTest;
  if (index >= tokens.length) { success.add(false); exec.test.abort('Provide one distinct test-player access token per iteration'); return; }
  const headers = { Authorization: `Bearer ${tokens[index]}`, 'Content-Type': 'application/json' };
  const started = Date.now();
  let passed = false;
  try {
    const intentRes = http.post(`${api}/v1/evidence/uploads`, JSON.stringify({ mediaType: __ENV.MEDIA_TYPE || 'image/jpeg', byteSize: body.byteLength, sha256: digest }), { headers, tags: { name: 'create_evidence' } });
    if (!check(intentRes, { 'intent created': r => r.status === 201 })) return;
    const intent = intentRes.json('data');
    const put = http.put(intent.uploadUrl, body, { headers: intent.requiredHeaders, redirects: 0, tags: { name: 'private_object_put' }, timeout: '60s' });
    if (!check(put, { 'object uploaded': r => r.status >= 200 && r.status < 300 })) return;
    const complete = http.post(`${api}/v1/evidence/uploads/${intent.id}/complete`, null, { headers, tags: { name: 'complete_evidence' } });
    if (!check(complete, { 'verification queued': r => r.status === 200 })) return;
    let delay = 2;
    while (Date.now() - started < 120000) {
      sleep(delay + Math.random());
      const state = http.get(`${api}/v1/evidence/uploads/${intent.id}`, { headers, tags: { name: 'poll_evidence' } });
      if (state.status === 429 || state.status === 503) { sleep(Math.min(30, Number(state.headers['Retry-After'] || 5))); continue; }
      if (state.status !== 200) return;
      const evidence = state.json('data');
      if (evidence.ready) { passed = true; readyTime.add(Date.now() - started); return; }
      if (['failed', 'rejected', 'expired'].includes(evidence.status)) return;
      delay = Math.min(10, delay * 1.5);
    }
  } finally { success.add(passed); }
}

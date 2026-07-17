import http from "k6/http";
import exec from "k6/execution";
import { Rate } from "k6/metrics";

const rate = integerEnv("RATE", 100);
const durationSeconds = integerEnv("DURATION_SECONDS", 10);
const sessionCookies = secretList(__ENV.SESSION_COOKIE, "SESSION_COOKIE");
const csrfTokens = secretList(__ENV.CSRF, "CSRF");

guardLoadTest();

if (sessionCookies.length !== csrfTokens.length) {
  throw new Error(
    "SESSION_COOKIE and CSRF must contain the same number of values",
  );
}

// One user receives four initial generation tokens. Requiring enough distinct
// sessions prevents the application rate limit from turning this into a 429 test.
const requiredSessions = Math.ceil((rate * durationSeconds) / 4);
if (sessionCookies.length < requiredSessions) {
  throw new Error(
    `the ${rate} RPS x ${durationSeconds}s profile needs at least ${requiredSessions} distinct test sessions; ` +
      `received ${sessionCookies.length}`,
  );
}

const baseURL = loadBaseURL();
const modelRevision = requiredEnv("MODEL_REVISION");
const modelID = __ENV.MODEL_ID || "openrouter-gpt-image-1";
const aspectRatio = __ENV.ASPECT_RATIO || "1:1";
const resolution = __ENV.RESOLUTION || "1K";
const batchCreated = new Rate("batch_created");

export const options = {
  discardResponseBodies: true,
  scenarios: {
    batch_create: {
      executor: "constant-arrival-rate",
      rate,
      timeUnit: "1s",
      duration: `${durationSeconds}s`,
      preAllocatedVUs: Math.max(50, rate * 2),
      maxVUs: Math.max(100, rate * 4),
      gracefulStop: "5s",
    },
  },
  thresholds: {
    batch_created: ["rate>0.99"],
    "http_req_duration{scenario:batch_create}": ["p(95)<250"],
    "http_req_failed{scenario:batch_create}": ["rate<0.01"],
    dropped_iterations: ["count==0"],
  },
};

export default function () {
  const index = exec.scenario.iterationInTest % sessionCookies.length;
  const idempotencyKey = `k6-${exec.scenario.iterationInTest}-${Date.now()}`;
  const response = http.post(
    `${baseURL}/api/v1/generations`,
    JSON.stringify({
      model_id: modelID,
      capability_revision: modelRevision,
      prompt: `Cornfield isolated load test ${exec.scenario.iterationInTest}`,
      aspect_ratio: aspectRatio,
      resolution,
      draw_count: 1,
      input_asset_ids: [],
    }),
    {
      headers: {
        "Content-Type": "application/json",
        Cookie: sessionCookies[index],
        "Idempotency-Key": idempotencyKey,
        "X-CSRF-Token": csrfTokens[index],
      },
      responseType: "none",
      tags: { endpoint: "create_generation" },
    },
  );

  batchCreated.add(response.status === 201);
}

function guardLoadTest() {
  if (__ENV.ALLOW_LOAD_TEST !== "true") {
    throw new Error("refusing to run: set ALLOW_LOAD_TEST=true explicitly");
  }
  if (__ENV.CONFIRM_MOCK_PROVIDER !== "true") {
    throw new Error(
      "refusing to create billable jobs: use an isolated mock deployment and set CONFIRM_MOCK_PROVIDER=true",
    );
  }
}

function requiredEnv(name) {
  const value = (__ENV[name] || "").trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function loadBaseURL() {
  const value = requiredEnv("BASE_URL").replace(/\/+$/, "");
  const secureOrigin =
    /^https:\/\/[^/?#]+$/.test(value) && !value.includes("@");
  const loopbackOrigin =
    /^http:\/\/(localhost|127\.0\.0\.1|\[::1\])(?::\d+)?$/.test(value);
  if (!secureOrigin && !loopbackOrigin) {
    throw new Error(
      "BASE_URL must be an HTTPS origin or an HTTP loopback origin, without credentials or a path",
    );
  }
  return value;
}

function integerEnv(name, fallback) {
  const raw = (__ENV[name] || `${fallback}`).trim();
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 1) {
    throw new Error(`${name} must be a positive integer`);
  }
  return value;
}

function secretList(raw, name) {
  const value = (raw || "").trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  if (!value.startsWith("[")) {
    return [value];
  }
  let parsed;
  try {
    parsed = JSON.parse(value);
  } catch (_) {
    throw new Error(`${name} must be a string or a JSON array of strings`);
  }
  if (
    !Array.isArray(parsed) ||
    parsed.length === 0 ||
    parsed.some((item) => typeof item !== "string" || !item.trim())
  ) {
    throw new Error(
      `${name} must be a non-empty JSON array of non-empty strings`,
    );
  }
  return parsed.map((item) => item.trim());
}

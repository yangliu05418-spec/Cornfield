import sse from "k6/x/sse";
import exec from "k6/execution";
import { Counter, Rate, Trend } from "k6/metrics";

guardLoadTest();

const baseURL = loadBaseURL();
const sessionCookies = secretList(__ENV.SESSION_COOKIE, "SESSION_COOKIE");
const virtualUsers = integerEnv("SSE_CONNECTIONS", 200);
const durationSeconds = integerEnv("DURATION_SECONDS", 30);
const requiredSessions = Math.ceil(virtualUsers / 4);
if (sessionCookies.length < requiredSessions) {
  throw new Error(
    `${virtualUsers} SSE connections require at least ${requiredSessions} distinct user sessions because the API caps each user at four; received ${sessionCookies.length}`,
  );
}
const opened = new Counter("sse_connections_opened");
const openSuccess = new Rate("sse_open_success");
const connectErrors = new Counter("sse_connect_errors");
const unexpectedCloses = new Counter("sse_unexpected_closes");
const connectLatency = new Trend("sse_connect_latency_ms", true);

export const options = {
  scenarios: {
    sse_200: {
      executor: "constant-vus",
      vus: virtualUsers,
      duration: `${durationSeconds}s`,
      gracefulStop: "0s",
    },
  },
  thresholds: {
    sse_connections_opened: [`count>=${virtualUsers}`],
    sse_open_success: ["rate>0.99"],
    sse_connect_errors: ["count==0"],
    sse_unexpected_closes: ["count==0"],
    sse_connect_latency_ms: ["p(95)<1000"],
  },
};

export default function () {
  const cookie = sessionCookies[(exec.vu.idInTest - 1) % sessionCookies.length];
  const startedAt = Date.now();
  let connectionOpened = false;
  let connectOutcomeRecorded = false;

  sse.open(
    `${baseURL}/api/v1/events`,
    {
      method: "GET",
      headers: {
        Accept: "text/event-stream",
        Cookie: cookie,
      },
      tags: { endpoint: "events" },
    },
    (client) => {
      client.on("open", () => {
        connectionOpened = true;
        connectOutcomeRecorded = true;
        opened.add(1);
        openSuccess.add(true);
        connectLatency.add(Date.now() - startedAt);
      });
      client.on("error", () => {
        if (!connectOutcomeRecorded) {
          connectOutcomeRecorded = true;
          connectErrors.add(1);
          openSuccess.add(false);
        }
      });
    },
  );

  if (!connectOutcomeRecorded) {
    connectErrors.add(1);
    openSuccess.add(false);
  }
  if (
    connectionOpened &&
    Date.now() - startedAt < Math.max(1, durationSeconds - 1) * 1000
  ) {
    unexpectedCloses.add(1);
  }
}

function guardLoadTest() {
  if (__ENV.ALLOW_LOAD_TEST !== "true") {
    throw new Error("refusing to run: set ALLOW_LOAD_TEST=true explicitly");
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

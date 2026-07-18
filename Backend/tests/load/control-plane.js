import http from "k6/http";
import ws from "k6/ws";
import {check, sleep} from "k6";

const baseURL = __ENV.BASE_URL || "http://127.0.0.1:8080";
const realtimeURL = (__ENV.REALTIME_URL || "ws://127.0.0.1:8080/v1/realtime/connect");
const accessTokens = JSON.parse(__ENV.ACCESS_TOKENS_JSON || "[]");

export const options = {
  scenarios: {
    peak_online: {
      executor: "constant-vus",
      vus: Number(__ENV.VUS || 100),
      duration: __ENV.DURATION || "5m",
      gracefulStop: "15s",
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<200"],
    http_req_failed: ["rate<0.01"],
    checks: ["rate>0.99"],
    ws_connecting: ["p(95)<1000"],
  },
};

export default function () {
  const responses = http.batch([
    ["GET", `${baseURL}/health/live`],
    ["GET", `${baseURL}/v1/game-servers?limit=50`],
    ["GET", `${baseURL}/v1/p2p-rooms?state=LOBBY&limit=50`],
    ["GET", `${baseURL}/v1/client/config`],
  ]);
  check(responses[0], {"liveness is 200": (response) => response.status === 200});
  check(responses[1], {"server directory is 200": (response) => response.status === 200});
  check(responses[2], {"room directory is 200": (response) => response.status === 200});
  check(responses[3], {"client config is 200": (response) => response.status === 200});

  if (accessTokens.length > 0) {
    const token = accessTokens[(__VU - 1) % accessTokens.length];
    const response = ws.connect(realtimeURL, {headers: {Authorization: `Bearer ${token}`}}, (socket) => {
      socket.setTimeout(() => socket.close(), 30000);
    });
    check(response, {"websocket upgrade is 101": (result) => result && result.status === 101});
  }
  sleep(1);
}

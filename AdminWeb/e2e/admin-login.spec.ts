import {expect, test, type Page, type Route} from "@playwright/test";

const testSiteKey = "1x00000000000000000000AA";

test("fails closed when Turnstile is not configured", async ({page}) => {
  await mockAdminAPI(page, {configured: false});
  await page.goto("/login");

  await expect(page.getByRole("heading", {name: "登录管理控制台"})).toBeVisible();
  await expect(page.getByText("管理员登录尚未启用")).toBeVisible();
  await expect(page.getByRole("button", {name: "继续验证"})).toBeDisabled();
});

test("completes Turnstile, password, MFA, and permission-filtered navigation", async ({page}) => {
  await mockAdminAPI(page, {configured: true});
  await page.goto("/login");

  await page.getByLabel("管理员账号").fill("viewer@example.com");
  await page.getByLabel("密码").fill("correct-horse-battery-staple");
  await expect(page.getByRole("button", {name: "继续验证"})).toBeEnabled({timeout: 30_000});
  await page.getByRole("button", {name: "继续验证"}).click();

  await expect(page.getByRole("heading", {name: "验证动态验证码"})).toBeVisible();
  await page.getByLabel("动态验证码或恢复码").fill("123456");
  await page.getByRole("button", {name: "验证并进入控制台"}).click();

  await expect(page).toHaveURL("/");
  await expect(page.getByRole("heading", {name: "运营总览"})).toBeVisible();
  await expect(page.getByRole("link", {name: "系统设置"})).toBeVisible();
  await expect(page.getByRole("link", {name: "玩家管理"})).toHaveCount(0);
});

async function mockAdminAPI(page: Page, options: {configured: boolean}) {
  await page.route("**/v1/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    switch (path) {
      case "/v1/admin/auth/refresh":
        return json(route, 401, {
          error: {code: "ADMIN_UNAUTHORIZED", message: "Authentication required.", details: {}},
          request_id: "req_e2e_refresh"
        });
      case "/v1/admin/auth/config":
        return success(route, {
          turnstile: {
            configured: options.configured,
            site_key: options.configured ? testSiteKey : "",
            action: "admin_login"
          }
        });
      case "/v1/admin/auth/login": {
        const body = request.postDataJSON() as Record<string, string>;
        expect(body.username).toBe("viewer@example.com");
        expect(body.password).toBe("correct-horse-battery-staple");
        expect(body.turnstile_token).toContain("DUMMY");
        return success(route, {
          mfa_required: true,
          challenge_token: "amc_e2e",
          expires_at: new Date(Date.now() + 300_000).toISOString()
        });
      }
      case "/v1/admin/auth/mfa/verify":
        return success(route, {
          access_token: "e2e-access-token",
          access_token_expires_at: new Date(Date.now() + 900_000).toISOString(),
          admin: {
            admin_id: "adm_e2e",
            username: "viewer@example.com",
            display_name: "E2E Viewer",
            roles: ["VIEWER"],
            permissions: ["dashboard.read", "settings.read"]
          }
        });
      case "/v1/admin/dashboard/summary":
        return success(route, {
          online_players: 3,
          active_p2p_rooms: 1,
          online_game_servers: 1,
          ready_relay_nodes: 1,
          active_relay_allocations: 0,
          unresolved_risk_events: 0,
          active_invite_codes: 1,
          active_admin_sessions: 1,
          generated_at: new Date().toISOString()
        });
      case "/v1/admin/dashboard/alerts":
        return success(route, {items: []});
      case "/v1/admin/dashboard/timeseries":
        return success(route, {items: [], period: "24h"});
      default:
        return json(route, 404, {
          error: {code: "NOT_FOUND", message: "Not found.", details: {}},
          request_id: "req_e2e_not_found"
        });
    }
  });
}

function success(route: Route, data: unknown) {
  return json(route, 200, {data, request_id: "req_e2e"});
}

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

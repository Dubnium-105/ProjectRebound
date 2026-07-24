import {
  Authenticated,
  Refine
} from "@refinedev/core";
import {useNotificationProvider} from "@refinedev/antd";
import routerProvider, {
  CatchAllNavigate,
  DocumentTitleHandler,
  NavigateToResource,
  UnsavedChangesNotifier
} from "@refinedev/react-router";
import {
  ApartmentOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  AuditOutlined,
  GlobalOutlined,
  KeyOutlined,
  LinkOutlined,
  RocketOutlined,
  SafetyCertificateOutlined,
  TeamOutlined,
  UserSwitchOutlined,
  PartitionOutlined,
  SettingOutlined
} from "@ant-design/icons";
import {Spin} from "antd";
import {lazy, Suspense} from "react";
import {Navigate, Outlet, Route, Routes} from "react-router";
import {dataProvider} from "./api/data-provider";
import {accessControlProvider, authProvider} from "./auth/provider";
import {AdminLayout} from "./components/AdminLayout";

const DashboardPage = lazy(() =>
  import("./pages/DashboardPage").then((module) => ({default: module.DashboardPage}))
);
const InviteCodesPage = lazy(() =>
  import("./pages/InviteCodesPage").then((module) => ({default: module.InviteCodesPage}))
);
const LoginPage = lazy(() =>
  import("./pages/LoginPage").then((module) => ({default: module.LoginPage}))
);
const PlayerShowPage = lazy(() =>
  import("./pages/PlayerShowPage").then((module) => ({default: module.PlayerShowPage}))
);
const PlayersListPage = lazy(() =>
  import("./pages/PlayersListPage").then((module) => ({default: module.PlayersListPage}))
);
const RiskEventsPage = lazy(() =>
  import("./pages/RiskEventsPage").then((module) => ({default: module.RiskEventsPage}))
);
const SessionsPage = lazy(() =>
  import("./pages/SessionsPage").then((module) => ({default: module.SessionsPage}))
);
const AuditLogsPage = lazy(() =>
  import("./pages/AuditLogsPage").then((module) => ({default: module.AuditLogsPage}))
);
const LoginAuditPage = lazy(() =>
  import("./pages/LoginAuditPage").then((module) => ({default: module.LoginAuditPage}))
);
const P2PRoomsPage = lazy(() =>
  import("./pages/P2PRoomsPage").then((module) => ({default: module.P2PRoomsPage}))
);
const GameServersPage = lazy(() =>
  import("./pages/GameServersPage").then((module) => ({default: module.GameServersPage}))
);
const RelayNodesPage = lazy(() =>
  import("./pages/RelayNodesPage").then((module) => ({default: module.RelayNodesPage}))
);
const ConnectionsPage = lazy(() =>
  import("./pages/ConnectionsPage").then((module) => ({default: module.ConnectionsPage}))
);
const ReleasesPage = lazy(() =>
  import("./pages/ReleasesPage").then((module) => ({default: module.ReleasesPage}))
);
const AdministratorsPage = lazy(() =>
  import("./pages/AdministratorsPage").then((module) => ({default: module.AdministratorsPage}))
);
const RolesPage = lazy(() =>
  import("./pages/RolesPage").then((module) => ({default: module.RolesPage}))
);
const SettingsPage = lazy(() =>
  import("./pages/SettingsPage").then((module) => ({default: module.SettingsPage}))
);

export function App() {
  return (
    <Refine
      routerProvider={routerProvider}
      dataProvider={dataProvider}
      authProvider={authProvider}
      accessControlProvider={accessControlProvider}
      notificationProvider={useNotificationProvider}
      resources={[
        {
          name: "dashboard",
          list: "/",
          meta: {label: "运营总览", icon: <DashboardOutlined />}
        },
        {
          name: "players",
          list: "/players",
          show: "/players/:id",
          meta: {label: "玩家管理", icon: <TeamOutlined />}
        },
        {
          name: "invite-codes",
          list: "/invite-codes",
          meta: {label: "邀请码", icon: <KeyOutlined />}
        },
        {
          name: "p2p-rooms",
          list: "/online/p2p-rooms",
          meta: {label: "P2P 房间", icon: <ApartmentOutlined />}
        },
        {
          name: "game-servers",
          list: "/online/game-servers",
          meta: {label: "Dedicated Server", icon: <CloudServerOutlined />}
        },
        {
          name: "relay-nodes",
          list: "/online/relay-nodes",
          meta: {label: "中继节点", icon: <GlobalOutlined />}
        },
        {
          name: "connections",
          list: "/online/connections",
          meta: {label: "活动连接", icon: <LinkOutlined />}
        },
        {
          name: "releases",
          list: "/releases",
          meta: {label: "客户端发布", icon: <RocketOutlined />}
        },
        {
          name: "risk-events",
          list: "/risk-events",
          meta: {label: "登录风险", icon: <SafetyCertificateOutlined />}
        },
        {
          name: "audit-logs",
          list: "/security/audit-logs",
          meta: {label: "操作审计", icon: <AuditOutlined />}
        },
        {
          name: "login-audit",
          list: "/security/login-audit",
          meta: {label: "登录审计", icon: <AuditOutlined />}
        },
        {
          name: "administrators",
          list: "/security/administrators",
          meta: {label: "管理员账号", icon: <UserSwitchOutlined />}
        },
        {
          name: "roles",
          list: "/security/roles",
          meta: {label: "角色与权限", icon: <PartitionOutlined />}
        },
        {
          name: "settings",
          list: "/settings",
          meta: {label: "系统设置", icon: <SettingOutlined />}
        }
      ]}
      options={{
        syncWithLocation: true,
        warnWhenUnsavedChanges: true,
        disableTelemetry: true,
        title: {text: "ProjectRebound 管理控制台"}
      }}
    >
      <Suspense fallback={<div className="route-loading"><Spin size="large" /></div>}>
        <Routes>
          <Route
            element={
              <Authenticated key="admin-protected" fallback={<Navigate to="/login" replace />}>
                <AdminLayout />
              </Authenticated>
            }
          >
            <Route index element={<DashboardPage />} />
            <Route path="/players" element={<PlayersListPage />} />
            <Route path="/players/:id" element={<PlayerShowPage />} />
            <Route path="/invite-codes" element={<InviteCodesPage />} />
            <Route path="/online/p2p-rooms" element={<P2PRoomsPage />} />
            <Route path="/online/game-servers" element={<GameServersPage />} />
            <Route path="/online/relay-nodes" element={<RelayNodesPage />} />
            <Route path="/online/connections" element={<ConnectionsPage />} />
            <Route path="/releases" element={<ReleasesPage />} />
            <Route path="/risk-events" element={<RiskEventsPage />} />
            <Route path="/security/sessions" element={<SessionsPage />} />
            <Route path="/security/audit-logs" element={<AuditLogsPage />} />
            <Route path="/security/login-audit" element={<LoginAuditPage />} />
            <Route path="/security/administrators" element={<AdministratorsPage />} />
            <Route path="/security/roles" element={<RolesPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<CatchAllNavigate to="/" />} />
          </Route>
          <Route
            element={
              <Authenticated key="admin-login" fallback={<Outlet />}>
                <NavigateToResource resource="dashboard" />
              </Authenticated>
            }
          >
            <Route path="/login" element={<LoginPage />} />
          </Route>
        </Routes>
      </Suspense>
      <UnsavedChangesNotifier />
      <DocumentTitleHandler />
    </Refine>
  );
}

import { tr } from "./i18n";
import { Authenticated, Refine } from "@refinedev/core";
import { useNotificationProvider } from "@refinedev/antd";
import routerProvider, { CatchAllNavigate, DocumentTitleHandler, NavigateToResource, UnsavedChangesNotifier } from "@refinedev/react-router";
import { ApartmentOutlined, CloudServerOutlined, DashboardOutlined, AuditOutlined, GlobalOutlined, KeyOutlined, LinkOutlined, RocketOutlined, SafetyCertificateOutlined, TeamOutlined, UserSwitchOutlined, PartitionOutlined, SettingOutlined } from "@ant-design/icons";
import { Spin } from "antd";
import { lazy, Suspense } from "react";
import { Navigate, Outlet, Route, Routes } from "react-router";
import { dataProvider } from "./api/data-provider";
import { accessControlProvider, authProvider } from "./auth/provider";
import { AdminLayout } from "./components/AdminLayout";
const DashboardPage = lazy(() => import("./pages/DashboardPage").then((module) => ({ default: module.DashboardPage })));
const InviteCodesPage = lazy(() => import("./pages/InviteCodesPage").then((module) => ({ default: module.InviteCodesPage })));
const LoginPage = lazy(() => import("./pages/LoginPage").then((module) => ({ default: module.LoginPage })));
const PlayerShowPage = lazy(() => import("./pages/PlayerShowPage").then((module) => ({ default: module.PlayerShowPage })));
const PlayersListPage = lazy(() => import("./pages/PlayersListPage").then((module) => ({ default: module.PlayersListPage })));
const RiskEventsPage = lazy(() => import("./pages/RiskEventsPage").then((module) => ({ default: module.RiskEventsPage })));
const SessionsPage = lazy(() => import("./pages/SessionsPage").then((module) => ({ default: module.SessionsPage })));
const AuditLogsPage = lazy(() => import("./pages/AuditLogsPage").then((module) => ({ default: module.AuditLogsPage })));
const LoginAuditPage = lazy(() => import("./pages/LoginAuditPage").then((module) => ({ default: module.LoginAuditPage })));
const P2PRoomsPage = lazy(() => import("./pages/P2PRoomsPage").then((module) => ({ default: module.P2PRoomsPage })));
const GameServersPage = lazy(() => import("./pages/GameServersPage").then((module) => ({ default: module.GameServersPage })));
const RelayNodesPage = lazy(() => import("./pages/RelayNodesPage").then((module) => ({ default: module.RelayNodesPage })));
const ConnectionsPage = lazy(() => import("./pages/ConnectionsPage").then((module) => ({ default: module.ConnectionsPage })));
const ReleasesPage = lazy(() => import("./pages/ReleasesPage").then((module) => ({ default: module.ReleasesPage })));
const AdministratorsPage = lazy(() => import("./pages/AdministratorsPage").then((module) => ({ default: module.AdministratorsPage })));
const RolesPage = lazy(() => import("./pages/RolesPage").then((module) => ({ default: module.RolesPage })));
const SettingsPage = lazy(() => import("./pages/SettingsPage").then((module) => ({ default: module.SettingsPage })));
export function App() {
    return (<Refine routerProvider={routerProvider} dataProvider={dataProvider} authProvider={authProvider} accessControlProvider={accessControlProvider} notificationProvider={useNotificationProvider} resources={[
            {
                name: "dashboard",
                list: "/",
                meta: { label: tr("\u8FD0\u8425\u603B\u89C8"), icon: <DashboardOutlined /> }
            },
            {
                name: "players",
                list: "/players",
                show: "/players/:id",
                meta: { label: tr("\u73A9\u5BB6\u7BA1\u7406"), icon: <TeamOutlined /> }
            },
            {
                name: "invite-codes",
                list: "/invite-codes",
                meta: { label: tr("\u9080\u8BF7\u7801"), icon: <KeyOutlined /> }
            },
            {
                name: "p2p-rooms",
                list: "/online/p2p-rooms",
                meta: { label: tr("P2P \u623F\u95F4"), icon: <ApartmentOutlined /> }
            },
            {
                name: "game-servers",
                list: "/online/game-servers",
                meta: { label: "Dedicated Server", icon: <CloudServerOutlined /> }
            },
            {
                name: "relay-nodes",
                list: "/online/relay-nodes",
                meta: { label: tr("\u4E2D\u7EE7\u8282\u70B9"), icon: <GlobalOutlined /> }
            },
            {
                name: "connections",
                list: "/online/connections",
                meta: { label: tr("\u6D3B\u52A8\u8FDE\u63A5"), icon: <LinkOutlined /> }
            },
            {
                name: "releases",
                list: "/releases",
                meta: { label: tr("\u5BA2\u6237\u7AEF\u53D1\u5E03"), icon: <RocketOutlined /> }
            },
            {
                name: "risk-events",
                list: "/risk-events",
                meta: { label: tr("\u767B\u5F55\u98CE\u9669"), icon: <SafetyCertificateOutlined /> }
            },
            {
                name: "audit-logs",
                list: "/security/audit-logs",
                meta: { label: tr("\u64CD\u4F5C\u5BA1\u8BA1"), icon: <AuditOutlined /> }
            },
            {
                name: "login-audit",
                list: "/security/login-audit",
                meta: { label: tr("\u767B\u5F55\u5BA1\u8BA1"), icon: <AuditOutlined /> }
            },
            {
                name: "administrators",
                list: "/security/administrators",
                meta: { label: tr("\u7BA1\u7406\u5458\u8D26\u53F7"), icon: <UserSwitchOutlined /> }
            },
            {
                name: "roles",
                list: "/security/roles",
                meta: { label: tr("\u89D2\u8272\u4E0E\u6743\u9650"), icon: <PartitionOutlined /> }
            },
            {
                name: "settings",
                list: "/settings",
                meta: { label: tr("\u7CFB\u7EDF\u8BBE\u7F6E"), icon: <SettingOutlined /> }
            }
        ]} options={{
            syncWithLocation: true,
            warnWhenUnsavedChanges: true,
            disableTelemetry: true,
            title: { text: tr("ProjectRebound \u7BA1\u7406\u63A7\u5236\u53F0") }
        }}>
      <Suspense fallback={<div className="route-loading"><Spin size="large"/></div>}>
        <Routes>
          <Route element={<Authenticated key="admin-protected" fallback={<Navigate to="/login" replace/>}>
                <AdminLayout />
              </Authenticated>}>
            <Route index element={<DashboardPage />}/>
            <Route path="/players" element={<PlayersListPage />}/>
            <Route path="/players/:id" element={<PlayerShowPage />}/>
            <Route path="/invite-codes" element={<InviteCodesPage />}/>
            <Route path="/online/p2p-rooms" element={<P2PRoomsPage />}/>
            <Route path="/online/game-servers" element={<GameServersPage />}/>
            <Route path="/online/relay-nodes" element={<RelayNodesPage />}/>
            <Route path="/online/connections" element={<ConnectionsPage />}/>
            <Route path="/releases" element={<ReleasesPage />}/>
            <Route path="/risk-events" element={<RiskEventsPage />}/>
            <Route path="/security/sessions" element={<SessionsPage />}/>
            <Route path="/security/audit-logs" element={<AuditLogsPage />}/>
            <Route path="/security/login-audit" element={<LoginAuditPage />}/>
            <Route path="/security/administrators" element={<AdministratorsPage />}/>
            <Route path="/security/roles" element={<RolesPage />}/>
            <Route path="/settings" element={<SettingsPage />}/>
            <Route path="*" element={<CatchAllNavigate to="/"/>}/>
          </Route>
          <Route element={<Authenticated key="admin-login" fallback={<Outlet />}>
                <NavigateToResource resource="dashboard"/>
              </Authenticated>}>
            <Route path="/login" element={<LoginPage />}/>
          </Route>
        </Routes>
      </Suspense>
      <UnsavedChangesNotifier />
      <DocumentTitleHandler />
    </Refine>);
}

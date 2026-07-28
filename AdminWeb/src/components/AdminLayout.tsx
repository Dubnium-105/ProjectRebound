import { tr } from "../i18n";
import { ApartmentOutlined, AuditOutlined, CloudServerOutlined, DashboardOutlined, GlobalOutlined, KeyOutlined, LinkOutlined, RocketOutlined, LockOutlined, LogoutOutlined, MenuFoldOutlined, MenuUnfoldOutlined, SafetyCertificateOutlined, TeamOutlined, UserSwitchOutlined, PartitionOutlined, SettingOutlined, UserOutlined } from "@ant-design/icons";
import { useGetIdentity, useLogout } from "@refinedev/core";
import { Avatar, Button, Dropdown, Grid, Layout, Menu, Space, Tag, Typography } from "antd";
import { useMemo, useState } from "react";
import { Link, Outlet, useLocation, useNavigate } from "react-router";
import { authClient } from "../api/client";
import { LanguageSwitcher } from "./LanguageSwitcher";
const { Header, Sider, Content } = Layout;
type Identity = {
    id: string;
    name: string;
    username: string;
    roles: string[];
};
export function AdminLayout() {
    const screens = Grid.useBreakpoint();
    const [collapsed, setCollapsed] = useState(false);
    const location = useLocation();
    const navigate = useNavigate();
    const { data: identity } = useGetIdentity<Identity>();
    const logout = useLogout();
    const permissions = authClient.permissions();
    const menuItems = useMemo(() => [
        {
            key: "/",
            icon: <DashboardOutlined />,
            label: <Link to="/">{tr("\u8FD0\u8425\u603B\u89C8")}</Link>,
            visible: permissions.includes("dashboard.read")
        },
        {
            key: "/players",
            icon: <TeamOutlined />,
            label: <Link to="/players">{tr("\u73A9\u5BB6\u7BA1\u7406")}</Link>,
            visible: permissions.includes("players.read")
        },
        {
            key: "/invite-codes",
            icon: <KeyOutlined />,
            label: <Link to="/invite-codes">{tr("\u9080\u8BF7\u7801")}</Link>,
            visible: permissions.includes("invite_codes.read")
        },
        {
            key: "/online/p2p-rooms",
            icon: <ApartmentOutlined />,
            label: <Link to="/online/p2p-rooms">{tr("P2P \u623F\u95F4")}</Link>,
            visible: permissions.includes("rooms.read")
        },
        {
            key: "/online/game-servers",
            icon: <CloudServerOutlined />,
            label: <Link to="/online/game-servers">Dedicated Server</Link>,
            visible: permissions.includes("game_servers.read")
        },
        {
            key: "/online/relay-nodes",
            icon: <GlobalOutlined />,
            label: <Link to="/online/relay-nodes">{tr("\u4E2D\u7EE7\u8282\u70B9")}</Link>,
            visible: permissions.includes("relay_nodes.read")
        },
        {
            key: "/online/connections",
            icon: <LinkOutlined />,
            label: <Link to="/online/connections">{tr("\u6D3B\u52A8\u8FDE\u63A5")}</Link>,
            visible: permissions.includes("connections.read")
        },
        {
            key: "/releases",
            icon: <RocketOutlined />,
            label: <Link to="/releases">{tr("\u5BA2\u6237\u7AEF\u53D1\u5E03")}</Link>,
            visible: permissions.includes("updates.read")
        },
        {
            key: "/risk-events",
            icon: <SafetyCertificateOutlined />,
            label: <Link to="/risk-events">{tr("\u767B\u5F55\u98CE\u9669")}</Link>,
            visible: permissions.includes("risk_events.read")
        },
        {
            key: "/security/audit-logs",
            icon: <AuditOutlined />,
            label: <Link to="/security/audit-logs">{tr("\u64CD\u4F5C\u5BA1\u8BA1")}</Link>,
            visible: permissions.includes("audit_logs.read")
        },
        {
            key: "/security/login-audit",
            icon: <AuditOutlined />,
            label: <Link to="/security/login-audit">{tr("\u767B\u5F55\u5BA1\u8BA1")}</Link>,
            visible: permissions.includes("audit_logs.read")
        },
        {
            key: "/security/administrators",
            icon: <UserSwitchOutlined />,
            label: <Link to="/security/administrators">{tr("\u7BA1\u7406\u5458\u8D26\u53F7")}</Link>,
            visible: permissions.includes("admins.read")
        },
        {
            key: "/security/roles",
            icon: <PartitionOutlined />,
            label: <Link to="/security/roles">{tr("\u89D2\u8272\u4E0E\u6743\u9650")}</Link>,
            visible: permissions.includes("admins.read")
        },
        {
            key: "/security/sessions",
            icon: <LockOutlined />,
            label: <Link to="/security/sessions">{tr("\u6211\u7684\u4F1A\u8BDD")}</Link>,
            visible: true
        },
        {
            key: "/settings",
            icon: <SettingOutlined />,
            label: <Link to="/settings">{tr("\u7CFB\u7EDF\u8BBE\u7F6E")}</Link>,
            visible: permissions.includes("settings.read")
        }
    ]
        .filter((item) => item.visible)
        .map(({ visible: _visible, ...item }) => item), [permissions]);
    const selectedKey = menuItems
        .map((item) => item.key)
        .filter((key) => key !== "/" && location.pathname.startsWith(key))
        .sort((left, right) => right.length - left.length)[0] ?? "/";
    return (<Layout className="admin-shell">
      <Sider className="admin-sider" collapsible collapsed={collapsed} trigger={null} breakpoint="lg" onBreakpoint={(broken) => setCollapsed(broken)} width={248} collapsedWidth={screens.xs ? 0 : 76}>
        <div className="brand-block">
          <div className="brand-mark">R</div>
          {!collapsed && (<div>
              <div className="brand-name">ProjectRebound</div>
              <div className="brand-subtitle">{tr("\u7BA1\u7406\u63A7\u5236\u53F0")}</div>
            </div>)}
        </div>
        <div className="sider-section-label">{collapsed ? "—" : tr("\u5DE5\u4F5C\u53F0")}</div>
        <Menu theme="dark" mode="inline" selectedKeys={[selectedKey]} items={menuItems} className="admin-menu"/>
        {!collapsed && (<div className="sider-security-note">
            <SafetyCertificateOutlined />
            <div>
              <strong>{tr("\u5B89\u5168\u5165\u53E3\u5DF2\u542F\u7528")}</strong>
              <span>Turnstile · TOTP · RBAC</span>
            </div>
          </div>)}
      </Sider>
      <Layout>
        <Header className="admin-header">
          <Button type="text" aria-label={collapsed ? tr("\u5C55\u5F00\u5BFC\u822A") : tr("\u6536\u8D77\u5BFC\u822A")} icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />} onClick={() => setCollapsed((value) => !value)}/>
          <div className="header-spacer"/>
          <LanguageSwitcher compact={Boolean(screens.xs)} />
          <Tag color="green" variant="filled" className="environment-tag">
            CONTROL PLANE
          </Tag>
          <Dropdown menu={{
            items: [
                {
                    key: "sessions",
                    icon: <LockOutlined />,
                    label: tr("\u6211\u7684\u4F1A\u8BDD"),
                    onClick: () => navigate("/security/sessions")
                },
                { type: "divider" },
                {
                    key: "logout",
                    danger: true,
                    icon: <LogoutOutlined />,
                    label: tr("\u5B89\u5168\u9000\u51FA"),
                    onClick: () => logout.mutate()
                }
            ]
        }} placement="bottomRight">
            <Button type="text" className="identity-button">
              <Space>
                <Avatar size={32} icon={<UserOutlined />}/>
                {!screens.xs && (<span className="identity-copy">
                    <Typography.Text strong>{identity?.name ?? tr("\u7BA1\u7406\u5458")}</Typography.Text>
                    <Typography.Text type="secondary">
                      {identity?.roles?.[0] ?? tr("\u5DF2\u8BA4\u8BC1")}
                    </Typography.Text>
                  </span>)}
              </Space>
            </Button>
          </Dropdown>
        </Header>
        <Content className="admin-content">
          <Outlet />
        </Content>
      </Layout>
    </Layout>);
}

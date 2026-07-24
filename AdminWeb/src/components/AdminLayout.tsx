import {
  ApartmentOutlined,
  AuditOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  GlobalOutlined,
  KeyOutlined,
  LinkOutlined,
  RocketOutlined,
  LockOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  SafetyCertificateOutlined,
  TeamOutlined,
  UserSwitchOutlined,
  PartitionOutlined,
  SettingOutlined,
  UserOutlined
} from "@ant-design/icons";
import {useGetIdentity, useLogout} from "@refinedev/core";
import {
  Avatar,
  Button,
  Dropdown,
  Grid,
  Layout,
  Menu,
  Space,
  Tag,
  Typography
} from "antd";
import {useMemo, useState} from "react";
import {Link, Outlet, useLocation, useNavigate} from "react-router";
import {authClient} from "../api/client";

const {Header, Sider, Content} = Layout;

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
  const {data: identity} = useGetIdentity<Identity>();
  const logout = useLogout();
  const permissions = authClient.permissions();

  const menuItems = useMemo(
    () =>
      [
        {
          key: "/",
          icon: <DashboardOutlined />,
          label: <Link to="/">运营总览</Link>,
          visible: permissions.includes("dashboard.read")
        },
        {
          key: "/players",
          icon: <TeamOutlined />,
          label: <Link to="/players">玩家管理</Link>,
          visible: permissions.includes("players.read")
        },
        {
          key: "/invite-codes",
          icon: <KeyOutlined />,
          label: <Link to="/invite-codes">邀请码</Link>,
          visible: permissions.includes("invite_codes.read")
        },
        {
          key: "/online/p2p-rooms",
          icon: <ApartmentOutlined />,
          label: <Link to="/online/p2p-rooms">P2P 房间</Link>,
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
          label: <Link to="/online/relay-nodes">中继节点</Link>,
          visible: permissions.includes("relay_nodes.read")
        },
        {
          key: "/online/connections",
          icon: <LinkOutlined />,
          label: <Link to="/online/connections">活动连接</Link>,
          visible: permissions.includes("connections.read")
        },
        {
          key: "/releases",
          icon: <RocketOutlined />,
          label: <Link to="/releases">客户端发布</Link>,
          visible: permissions.includes("updates.read")
        },
        {
          key: "/risk-events",
          icon: <SafetyCertificateOutlined />,
          label: <Link to="/risk-events">登录风险</Link>,
          visible: permissions.includes("risk_events.read")
        },
        {
          key: "/security/audit-logs",
          icon: <AuditOutlined />,
          label: <Link to="/security/audit-logs">操作审计</Link>,
          visible: permissions.includes("audit_logs.read")
        },
        {
          key: "/security/login-audit",
          icon: <AuditOutlined />,
          label: <Link to="/security/login-audit">登录审计</Link>,
          visible: permissions.includes("audit_logs.read")
        },
        {
          key: "/security/administrators",
          icon: <UserSwitchOutlined />,
          label: <Link to="/security/administrators">管理员账号</Link>,
          visible: permissions.includes("admins.read")
        },
        {
          key: "/security/roles",
          icon: <PartitionOutlined />,
          label: <Link to="/security/roles">角色与权限</Link>,
          visible: permissions.includes("admins.read")
        },
        {
          key: "/security/sessions",
          icon: <LockOutlined />,
          label: <Link to="/security/sessions">我的会话</Link>,
          visible: true
        },
        {
          key: "/settings",
          icon: <SettingOutlined />,
          label: <Link to="/settings">系统设置</Link>,
          visible: permissions.includes("settings.read")
        }
      ]
        .filter((item) => item.visible)
        .map(({visible: _visible, ...item}) => item),
    [permissions]
  );

  const selectedKey =
    menuItems
      .map((item) => item.key)
      .filter((key) => key !== "/" && location.pathname.startsWith(key))
      .sort((left, right) => right.length - left.length)[0] ?? "/";

  return (
    <Layout className="admin-shell">
      <Sider
        className="admin-sider"
        collapsible
        collapsed={collapsed}
        trigger={null}
        breakpoint="lg"
        onBreakpoint={(broken) => setCollapsed(broken)}
        width={248}
        collapsedWidth={screens.xs ? 0 : 76}
      >
        <div className="brand-block">
          <div className="brand-mark">R</div>
          {!collapsed && (
            <div>
              <div className="brand-name">ProjectRebound</div>
              <div className="brand-subtitle">管理控制台</div>
            </div>
          )}
        </div>
        <div className="sider-section-label">{collapsed ? "—" : "工作台"}</div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selectedKey]}
          items={menuItems}
          className="admin-menu"
        />
        {!collapsed && (
          <div className="sider-security-note">
            <SafetyCertificateOutlined />
            <div>
              <strong>安全入口已启用</strong>
              <span>Turnstile · TOTP · RBAC</span>
            </div>
          </div>
        )}
      </Sider>
      <Layout>
        <Header className="admin-header">
          <Button
            type="text"
            aria-label={collapsed ? "展开导航" : "收起导航"}
            icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
            onClick={() => setCollapsed((value) => !value)}
          />
          <div className="header-spacer" />
          <Tag color="green" variant="filled" className="environment-tag">
            CONTROL PLANE
          </Tag>
          <Dropdown
            menu={{
              items: [
                {
                  key: "sessions",
                  icon: <LockOutlined />,
                  label: "我的会话",
                  onClick: () => navigate("/security/sessions")
                },
                {type: "divider"},
                {
                  key: "logout",
                  danger: true,
                  icon: <LogoutOutlined />,
                  label: "安全退出",
                  onClick: () => logout.mutate()
                }
              ]
            }}
            placement="bottomRight"
          >
            <Button type="text" className="identity-button">
              <Space>
                <Avatar size={32} icon={<UserOutlined />} />
                {!screens.xs && (
                  <span className="identity-copy">
                    <Typography.Text strong>{identity?.name ?? "管理员"}</Typography.Text>
                    <Typography.Text type="secondary">
                      {identity?.roles?.[0] ?? "已认证"}
                    </Typography.Text>
                  </span>
                )}
              </Space>
            </Button>
          </Dropdown>
        </Header>
        <Content className="admin-content">
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
}

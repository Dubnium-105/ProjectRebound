import {createContext, useContext, useMemo, useState, type ReactNode} from "react";
import {generatedEnglishCatalog} from "./i18n.generated";

export type AppLocale = "zh-CN" | "en-US";

const STORAGE_KEY = "projectrebound.admin.locale";

const englishCatalog: Record<string, string> = {
  ...generatedEnglishCatalog,
  "取消": "Cancel",
  "确认": "Confirm",
  "刷新": "Refresh",
  "重置": "Reset",
  "筛选": "Filter",
  "搜索": "Search",
  "操作": "Actions",
  "状态": "Status",
  "详情": "Details",
  "编辑": "Edit",
  "创建": "Create",
  "关闭": "Close",
  "撤销": "Revoke",
  "复制": "Copy",
  "下载": "Download",
  "区域": "Region",
  "模式": "Mode",
  "版本": "Version",
  "时间": "Time",
  "玩家": "Player",
  "房间": "Room",
  "人数": "Players",
  "原因": "Reason",
  "权限": "Permissions",
  "角色": "Roles",
  "管理员": "Administrator",
  "管理员账号": "Administrator account",
  "登录管理控制台": "Sign in to the admin console",
  "受保护的运营入口": "Protected operations portal",
  "管理入口安全说明": "Admin access security overview",
  "第一步，共两步": "Step 1 of 2",
  "第二步，共两步": "Step 2 of 2",
  "账号验证": "Account verification",
  "动态验证码": "Authenticator code",
  "动态验证码或恢复码": "Authenticator or recovery code",
  "登录安全验证": "Login security check",
  "继续验证": "Continue",
  "反自动化": "Bot protection",
  "强身份": "Strong authentication",
  "最小权限": "Least privilege",
  "可信网络": "Trusted network",
  "恢复码": "Recovery code",
  "密码 + TOTP / 恢复码": "Password + TOTP / recovery code",
  "服务端 RBAC + 审计": "Server-side RBAC + audit",
  "都有身份、有边界、有记录": "authenticated, bounded, and audited",
  "由 Cloudflare 提供": "Powered by Cloudflare",
  "工作台": "Workspace",
  "运营总览": "Operations overview",
  "玩家管理": "Players",
  "邀请码": "Invite codes",
  "P2P 房间": "P2P rooms",
  "中继节点": "Relay nodes",
  "活动连接": "Active connections",
  "客户端发布": "Client releases",
  "登录风险": "Login risks",
  "登录风险事件": "Login risk events",
  "操作审计": "Operation audit",
  "登录审计": "Login audit",
  "角色与权限": "Roles and permissions",
  "我的会话": "My sessions",
  "我的管理员会话": "My administrator sessions",
  "系统设置": "System settings",
  "管理控制台": "Admin console",
  "ProjectRebound 管理控制台": "ProjectRebound Admin Console",
  "安全入口已启用": "Secure access enabled",
  "展开导航": "Expand navigation",
  "收起导航": "Collapse navigation",
  "安全退出": "Sign out",
  "已认证": "Authenticated",
  "中文": "中文",
  "英文": "English",
  "语言": "Language",
  "密码": "Password",
  "中继节点异常": "Relay node issues",
  "存在离线或不健康的中继节点。": "One or more relay nodes are offline or unhealthy.",
  "专服心跳异常": "Dedicated server heartbeat issues",
  "存在离线或不健康的 Dedicated Server。": "One or more dedicated servers are offline or unhealthy.",
  "高优先级风险事件": "High-priority risk events",
  "存在尚未处理的高危或严重登录风险事件。": "High-severity or critical login risk events remain unresolved."
};

const fragmentEntries = Object.entries(englishCatalog)
  .filter(([source]) => /[\p{Script=Han}]/u.test(source))
  .sort(([left], [right]) => right.length - left.length);
const translationCache = new Map<string, string>();

let activeLocale: AppLocale = readStoredLocale();

type I18nContextValue = {
  locale: AppLocale;
  setLocale: (locale: AppLocale) => void;
  t: typeof tr;
};

const I18nContext = createContext<I18nContextValue | null>(null);

function readStoredLocale(): AppLocale {
  if (typeof window === "undefined") return "zh-CN";
  return window.localStorage.getItem(STORAGE_KEY) === "en-US" ? "en-US" : "zh-CN";
}

export function I18nProvider({children}: {children: ReactNode}) {
  const [locale, updateLocale] = useState<AppLocale>(activeLocale);
  const value = useMemo<I18nContextValue>(
    () => ({
      locale,
      setLocale(nextLocale) {
        activeLocale = nextLocale;
        window.localStorage.setItem(STORAGE_KEY, nextLocale);
        document.documentElement.lang = nextLocale;
        updateLocale(nextLocale);
      },
      t: tr
    }),
    [locale]
  );

  activeLocale = locale;
  document.documentElement.lang = locale;

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  const value = useContext(I18nContext);
  if (!value) {
    throw new Error("useI18n must be used inside I18nProvider");
  }
  return value;
}

export function localeTag(): AppLocale {
  return activeLocale;
}

export function turnstileLanguage(): "en" | "zh-cn" {
  return activeLocale === "en-US" ? "en" : "zh-cn";
}

export function tr(source: string): string {
  if (activeLocale !== "en-US") return source;
  const exact = englishCatalog[source];
  if (exact) return exact;
  const cached = translationCache.get(source);
  if (cached) return cached;

  let translated = source;
  for (const [fragment, replacement] of fragmentEntries) {
    if (translated.includes(fragment)) {
      translated = translated.replaceAll(fragment, replacement);
    }
  }
  translationCache.set(source, translated);
  return translated;
}

export function localizeSystemText(source: string, englishFallback: string): string {
  const translated = tr(source);
  if (activeLocale === "en-US" && /[\p{Script=Han}]/u.test(translated)) {
    return englishFallback;
  }
  return translated;
}

export function formatRequestError(message: string, requestId = "") {
  if (!requestId) return message;
  return activeLocale === "en-US"
    ? `${message} (Request ID: ${requestId})`
    : `${message}（请求编号：${requestId}）`;
}

export function isEnglish() {
  return activeLocale === "en-US";
}

export {englishCatalog};

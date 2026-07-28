import React from "react";
import ReactDOM from "react-dom/client";
import {App as AntApp, ConfigProvider} from "antd";
import enUS from "antd/locale/en_US";
import zhCN from "antd/locale/zh_CN";
import {BrowserRouter} from "react-router";
import {App} from "./App";
import {I18nProvider, useI18n} from "./i18n";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <I18nProvider>
      <LocalizedRoot />
    </I18nProvider>
  </React.StrictMode>
);

function LocalizedRoot() {
  const {locale} = useI18n();

  return (
    <ConfigProvider
      locale={locale === "en-US" ? enUS : zhCN}
      theme={{
        token: {
          colorPrimary: "#3268f1",
          colorInfo: "#3268f1",
          colorSuccess: "#1d9b73",
          colorWarning: "#d28a22",
          colorError: "#d84d55",
          borderRadius: 10,
          borderRadiusLG: 16,
          fontFamily:
            '"Inter", "SF Pro Text", "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif'
        },
        components: {
          Layout: {bodyBg: "#f4f6fa", headerBg: "#ffffff", siderBg: "#111827"},
          Table: {headerBg: "#f7f8fb", headerColor: "#667085"},
          Card: {headerFontSize: 15}
        }
      }}
    >
      <AntApp key={locale}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </AntApp>
    </ConfigProvider>
  );
}

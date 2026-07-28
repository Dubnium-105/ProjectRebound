import {fireEvent, render, screen} from "@testing-library/react";
import {describe, expect, it} from "vitest";
import {I18nProvider, localizeSystemText, tr, useI18n} from "./i18n";

function LocaleProbe() {
  const {locale, setLocale} = useI18n();
  return (
    <>
      <button onClick={() => setLocale("zh-CN")}>use-zh</button>
      <button onClick={() => setLocale("en-US")}>use-en</button>
      <span data-testid="locale">{locale}</span>
      <span data-testid="overview">{tr("运营总览")}</span>
      <span data-testid="dashboard-alert">{tr("中继节点异常")}</span>
      <span data-testid="dashboard-alert-summary">{tr("存在离线或不健康的中继节点。")}</span>
      <span data-testid="untranslated-system-text">
        {localizeSystemText("后端新增但尚未翻译的系统消息", "Untranslated system message")}
      </span>
    </>
  );
}

describe("administrator locale", () => {
  it("switches copy and persists the selected locale", () => {
    render(
      <I18nProvider>
        <LocaleProbe />
      </I18nProvider>
    );

    fireEvent.click(screen.getByText("use-zh"));
    expect(screen.getByTestId("overview")).toHaveTextContent("运营总览");

    fireEvent.click(screen.getByText("use-en"));
    expect(screen.getByTestId("locale")).toHaveTextContent("en-US");
    expect(screen.getByTestId("overview")).toHaveTextContent("Operations overview");
    expect(screen.getByTestId("dashboard-alert")).toHaveTextContent("Relay node issues");
    expect(screen.getByTestId("dashboard-alert-summary")).toHaveTextContent(
      "One or more relay nodes are offline or unhealthy."
    );
    expect(screen.getByTestId("untranslated-system-text")).toHaveTextContent("Untranslated system message");
    expect(window.localStorage.getItem("projectrebound.admin.locale")).toBe("en-US");
    expect(document.documentElement.lang).toBe("en-US");
  });
});

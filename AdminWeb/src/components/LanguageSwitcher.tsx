import { GlobalOutlined } from "@ant-design/icons";
import { Button, Dropdown } from "antd";
import { tr, useI18n, type AppLocale } from "../i18n";
export function LanguageSwitcher({ compact = false }: {
    compact?: boolean;
}) {
    const { locale, setLocale } = useI18n();
    const changeLanguage = (nextLocale: AppLocale) => {
        if (nextLocale !== locale)
            setLocale(nextLocale);
    };
    return (<Dropdown menu={{
            selectable: true,
            selectedKeys: [locale],
            items: [
                { key: "zh-CN", label: tr("\u4E2D\u6587"), onClick: () => changeLanguage("zh-CN") },
                { key: "en-US", label: "English", onClick: () => changeLanguage("en-US") }
            ]
        }} placement="bottomRight" trigger={["click"]}>
      <Button type="text" icon={<GlobalOutlined />} aria-label={locale === "en-US" ? "Language: English" : tr("\u8BED\u8A00\uFF1A\u4E2D\u6587")}>
        {compact ? null : locale === "en-US" ? "English" : tr("\u4E2D\u6587")}
      </Button>
    </Dropdown>);
}

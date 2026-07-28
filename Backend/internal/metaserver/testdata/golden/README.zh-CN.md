# 脱敏 Golden Packet

[English](README.md) | 简体中文

样本不包含真实 Steam ID、Access Token、Gate Ticket、玩家档案或配装。原生协议文件
采用小写十六进制，不包含四字节 TCP 长度前缀；QoS 文件包含完整 UDP 数据报。

- `gate.hex`：已脱敏的 Gate 握手 wrapper。
- `profile.hex`：玩家档案查询 wrapper。
- `party.hex`：Party 创建 wrapper。
- `matchmaking.hex`：匹配启动 wrapper。
- `qos.hex`：带三字节回显数据的合法 `0x59` QoS 请求。
- `login.json`：已脱敏的 Meta 会话请求。

替换这些文件前，真实抓包必须完成脱敏和人工审查。

# ksd — OpenWrt Killswitch 守护进程
<p align="left">
  <a href="./README.md"><img src="https://img.shields.io/badge/lang-Русский-blue.svg" alt="Русский"></a>
  <a href="./README.en.md"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
  <a href="./README.zh.md"><img src="https://img.shields.io/badge/lang-中文-brightgreen.svg" alt="中文"></a>
</p>
<p align="center">
  <img src="Project.png" alt="KSD" width="350">
</p>
<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/Platform-OpenWrt-orange" alt="OpenWrt">
  <img src="https://img.shields.io/badge/Status-v4.0--dev-yellow" alt="Status">
</p>
基于 nftables、用 Go 编写的 OpenWrt fail-close killswitch 守护进程。
设计用于配合 Passwall2：所有未打标记的流量一律阻断，
除非其目的地是允许的 VPS 地址。

## 功能

- 原子化应用 nftables 规则（nft 事务）
- 出错时自动回滚到 baseline
- 阻断 DNS / DoT / DoQ / DoH 泄漏
- 可选的 QUIC（UDP/443）阻断
- 通过 forward 钩子保护 LAN 客户端
- ARP 欺骗防护
- MSS clamp 与可选的 TTL 设置
- 定期校验内核中的规则
- 零外部依赖（CGO_ENABLED=0，静态编译）

## 编译

    go build -o ksd ./...

针对 OpenWrt 的交叉编译（以 ARM64 为例）：

    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ksd-arm64 ./...

## 安装

1. 将二进制文件复制到路由器：
       scp ksd root@192.168.1.1:/usr/sbin/ksd

2. 安装 UCI 配置：
       scp openwrt/killswitch.config root@192.168.1.1:/etc/config/killswitch

3. 安装 init 脚本：
       scp openwrt/ksd-boot root@192.168.1.1:/etc/init.d/ksd
       ssh root@192.168.1.1 'chmod +x /etc/init.d/ksd && /etc/init.d/ksd enable'

4. 启动：
       ssh root@192.168.1.1 '/etc/init.d/ksd start'

## 配置工具

交互式 UCI TUI 配置工具：

    scripts/ksd-configurator

无需手动执行 `uci set`，即可管理 `/etc/config/killswitch` 中的所有选项。
修改完成后，请使用「应用并重启」菜单项。

## 文档

- [CONFIG.md](CONFIG.md) — 所有 UCI 选项说明
- [BUILD.md](BUILD.md) — 编译细节
- [docs/OPERATOR.md](docs/OPERATOR.md) — 运维手册
- [docs/DESCRIPTION.zh.md](docs/DESCRIPTION.zh.md) — 中文说明

## 许可证

MIT License — 详见 [LICENSE](LICENSE)。

## 状态

- v3.5（shell）— 参考实现，2026-08-30 冻结
- v4.0（Go）— 积极开发中

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

> ⚠️ **请保持 UART 或第二个 SSH 会话开启。** 如果 ksd 阻断了访问，可从此处恢复。

1. 根据路由器架构（`uname -m`）下载对应二进制文件：

       wget https://github.com/Artronah1/KSD/releases/latest/download/ksd-arm64
       sha256sum ksd-arm64

   校验值应与发布页上的 `sha256` 一致。

2. 将文件复制到路由器：

       scp ksd-arm64 root@192.168.1.1:/tmp/ksd
       scp openwrt/killswitch.config root@192.168.1.1:/etc/config/killswitch
       scp openwrt/ksd-boot root@192.168.1.1:/etc/init.d/ksd-boot

3. 在路由器上安装：

       ssh root@192.168.1.1

       cp /tmp/ksd /usr/sbin/ksd
       chmod +x /usr/sbin/ksd /etc/init.d/ksd-boot
       /etc/init.d/ksd-boot enable

4. 检查配置 `/etc/config/killswitch`：

       nano /etc/config/killswitch

   确认以下项正确：`source_set`、`wan_interface` / `wan_device`、
   `allowed_iface`、`arp_protection` 和 `arp_gateway_mac`。

5. 首次运行：

       /usr/sbin/ksd install -config /etc/config/killswitch

6. 开机自启与启动：

       /etc/init.d/ksd enable
       /etc/init.d/ksd start

7. 验证：

       /usr/sbin/ksd status -config /etc/config/killswitch
       /usr/sbin/ksd self-test -config /etc/config/killswitch

   两条命令均应显示 `[OK]` 和 `Result: PASS`。

## 自动安装

通用安装脚本：自动检测架构、WAN、LAN 桥、网关及其 MAC、VPS 源集合；
生成 `/etc/config/killswitch`；下载对应二进制文件；安装服务。

**在路由器上：**

    wget -O /tmp/install.sh https://raw.githubusercontent.com/Artronah1/KSD/main/scripts/install.sh
    sh /tmp/install.sh

**选项：**

    sh /tmp/install.sh -v v1.1.4           # 指定版本
    sh /tmp/install.sh -f /tmp/ksd-arm64   # 使用本地二进制文件

**如果 `/etc/config/killswitch` 已存在** — 安装脚本会保留，不修改。
请**手动**检查 `source_set`、`wan_device`、`arp_gateway`。

**安装后** — 重启：

    /etc/init.d/ksd restart
   
## 配置工具

交互式 UCI TUI 配置工具：

    scripts/ksdc

无需手动执行 `uci set`，即可管理 `/etc/config/killswitch` 中的所有选项。
修改完成后，请使用「应用并重启」菜单项。

## 文档

- [CONFIG.md](CONFIG.md) — 所有 UCI 选项说明
- [BUILD.md](BUILD.md) — 编译细节
- [docs/OPERATOR.zh.md](docs/OPERATOR.zh.md) — 运维手册
- [docs/DESCRIPTION.zh.md](docs/DESCRIPTION.zh.md) — 项目说明

## 许可证

木兰公共许可证第 2 版（Mulan PubL v2）— 详见 [LICENSE](LICENSE)。

带**网络条款**的**强著佐权**许可证：衍生作品（包括通过网络提供服务）
必须以同一许可证分发，并提供源代码

## 状态

- v3.5（shell）— 参考实现，2026-08-30 冻结
- v4.0（Go）— 积极开发中

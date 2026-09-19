# ksd（Killswitch v4.0，Go 版）— 运维手册

**版本：** v4.0-dev  
**冻结日期：** 尚未冻结（参考实现：shell v3.5，冻结于 2026-08-30）  
**二进制 SHA-256：** `TODO` — 编译后计算：`sha256sum ksd`  
**目标设备：** Routerich AX3000 v1（MT7981，aarch64），OpenWrt 25.12.5

> **草稿状态。** 需要路由器上的真实数据或你做决定的地方都标了 `TODO`，文末有汇总清单，提交前请删掉。

---

## 这是什么

ksd 是用 Go 编写的常驻守护进程，基于 nftables 为 OpenWrt 实现 fail-close 防火墙。路由器发出的所有出站流量要么：

- 带有 Passwall2 打的标记（`allowed_mark`：`0x50535732`、`0x000000ff`）；
- 去往允许的 VPS 地址（动态集合 `vps_ipv4`）；
- 经由受信任接口发出（`allowed_iface`：`lo`、`br-lan`）；
- 属于已建立/相关（established/related）连接；
- 否则被阻断。

**核心特性：**

- 原子化应用规则集（单次 nft 事务）。
- 出错时回滚到 baseline；`full_fail_max`（默认 3）是防止周期性断网的熔断器。
- 校验由规则模型推导：每条规则都带 `comment`，内核状态与模型逐项比对（链参数、规则的存在性与**相对顺序**、计数器、动态集合内容）。不再有独立的「校验器」，因此不会与生成器产生漂移。
- 防护 DNS/DoT/DoQ/DoH 泄漏；可选的 QUIC（udp/443）阻断。
- 通过 forward 钩子保护 LAN 客户端。
- 可选的 WAN 口 ARP 防护。
- 分层 fail-close：`ks_emergency`（Layer 0）→ baseline → full。
- 零外部依赖，静态编译（`CGO_ENABLED=0`）。

---

## 与 shell v3.5 相比的变化

| 之前（shell） | 现在（Go） |
|---|---|
| cron 监控（`monitoring_*`） | 常驻循环：`poll_interval_sec` = 5（端点集合），`verify_interval_sec` = 60（规则完整性） |
| 磁盘上的 VPS 集合缓存、`cache_ttl_hours`、`cache_file` | 已删除：守护进程每 5 秒直接读取源集合 |
| `empty_source_threshold`（连续 N 次） | `empty_source_grace_sec` = 15（秒，与轮询频率无关） |
| `semantic_verify_enabled` | 已删除：校验始终开启，由模型推导 |
| `reset_checksum_on_change`、`checksum.md5` | 已删除：完整性依靠二进制分发，不做运行时自校验 |
| `monitor_after_stop` | 已删除：守护进程常驻 |
| `ntp_servers` 只在 baseline 生效 | 现在 **full** 模式下也生效 |
| `max_vps_elements`：静默截断 | 超限会**拒绝整次更新** |
| 每次变更都清 conntrack | 默认只在端点集合**缩小**时清（`flush_on_narrow_only=1`） |
| `dry_run`、`test`、`logread -e killswitch` | `dry-run`、`self-test`、`logread -e ksd` |
| `/var/run/` 下十几个文件 | 单一文件 `/var/run/ksd-state.json`（纯缓存，一个轮询周期内即可重新学习） |
| WAN 接口和网关写死在规则里 | 移入 `ks_wan` / `ks_wan_gw` 集合：WAN 变化只更新集合元素，不重建规则集 |
| `filter`/`forward`/`mangle` 优先级 | 默认 `-10` / `-15` / `-150`（fw4 用 0，所以 ksd 先执行） |

> **TODO：** 确认 shell v3.5 在生产环境中使用的优先级，必要时修改优先级那一行。同时确认锁文件（`/var/run/killswitch.lock`）已不再需要。

---

## 快速诊断

### 当前状态

```bash
ksd status
```

从状态文件和 status 输出中重点看：

- **Mode：** `full` — 完整防护，`baseline` — 最小防护。
- **Source：** `DOWN` — Passwall2 的源表不存在（ksd 会清 conntrack 并计入防抖）。
- **full_fail：** 进入 full 模式失败的次数。达到 `full_fail_max` 表示熔断器已触发。
- **Counters：** 被阻断的包数（`output_leak_drops`、`dns_leak_drops` 等）。

> **TODO：** 贴上真实的 `ksd status` 输出，并按实际字段补充说明。上面的描述依据的是 `/var/run/ksd-state.json` 的结构，而不是该命令的实际输出。

### 快速检查

```bash
ksd self-test
```

预期输出（取自 BUILD.md 的示例，请用真实构建验证）：

```
[OK]   table inet killswitch_table present
[OK]   live ruleset matches model (<nil>)
[OK]   VPS set vps_ipv4 present
[OK]   conntrack utility available
[OK]   DNS query to 1.1.1.1 blocked (expected)
Result: PASS
```

### 只生成规则不应用

```bash
ksd dry-run full        # 显示 full 规则集
ksd dry-run baseline    # 显示 baseline 规则集
```

### 日志与计数器

```bash
logread -e ksd | tail -n 50
nft list counters table inet killswitch_table
nft list set inet killswitch_table vps_ipv4 | head
```

日志输出到 stderr，由 procd 转发到 syslog。也可以通过 `log_file` 和 `log_max_kb`（默认 512，保留一代轮转）额外启用文件日志。

---

## 变更时该怎么做

### 1. 修改配置（UCI）

```bash
uci set killswitch.main.<选项>=<值>
uci commit killswitch
/etc/init.d/ksd restart
```

> **TODO：** 确认守护进程是在 `reload`（SIGHUP / procd 触发器）时重新读取配置，还是只有 `restart` 后才会读取。目前手册使用确定可行的 `restart`。`ksd` 直接解析 UCI 文件（不调用 `uci`），所以必须执行 `uci commit`，否则磁盘上不会有任何变化。

所有选项及默认值的完整说明见 `CONFIG.md`。

### 2. 修改代码（构建新版本）

```bash
## 1. 修改源码
cd ~/ksd
nano daemon.go

## 2. 检查
go vet ./...
go build ./...

## 3. 交叉编译
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o ksd-arm64 .
sha256sum ksd-arm64

## 4. 复制到路由器
scp ksd-arm64 root@192.168.1.1:/tmp/ksd-new

## 5. 在路由器上：替换二进制
cp /tmp/ksd-new /usr/sbin/ksd.new
chmod 755 /usr/sbin/ksd.new
mv /usr/sbin/ksd.new /usr/sbin/ksd

## 6. 应用前检查
ksd dry-run full
ksd self-test

## 7. 重启
/etc/init.d/ksd restart
ksd status
```

> **为什么用 `mv` 而不是直接 `cp` 覆盖 `/usr/sbin/ksd`：** 守护进程运行期间，写入其可执行文件会报 `Text file busy`。先复制到旁边，再原子重命名（同一文件系统内的 rename 对运行中的进程是安全的）。

守护进程停止后，规则仍保留在内核中（`force_stop` 默认为 0），因此即使在替换二进制期间，fail-close 也不会丢失。

如需完整重装规则：

```bash
/etc/init.d/ksd stop
/usr/sbin/ksd install -config /etc/config/killswitch
/etc/init.d/ksd start
```

> **TODO：** `ksd install` 具体做什么？BUILD.md 里没有列出这条命令（那里有 `run`、`dry-run`、`baseline`、`status`、`self-test`、`reset-counters`）。如果不需要，请删除这一段。

> **TODO（契约）：** 版本是否仍然与 `ASSURANCE.md` 中的 sha256 绑定？如果是，则在第 3 步之后更新「Artifact Identity」一节（版本、哈希、Go 版本、构建参数 `-trimpath -ldflags="-s -w"`），并与代码一起提交。

### 3. 规则校验（语义检查）

不再有开关：检查始终开启。要修改规则，只改**模型**（`ruleset.go`）。生成与校验出自同一来源，会一起变化。

---

## 首次运行与启动

不要一上来就 `service ksd start`。按以下顺序：

```bash
ksd dry-run baseline
ksd dry-run full
ksd baseline          # 只装 baseline，确认 SSH/DHCP 仍然可用
ksd status
service ksd start
sleep 5
ksd status
ksd self-test
```

全程请保留备用管理通道（串口，或不经过 killswitch 的路径）。如果 `br-lan` 不在 `allowed_iface` 里，从 LAN 侧 SSH 会被 output 链挡掉。

### 启动顺序

| START | 组件 | 作用 |
|---|---|---|
| 15 | `/etc/init.d/ksd-boot` | 安装 `ks_emergency`（静态文件，无逻辑、无依赖） |
| 20 | netifd | WAN 起来 |
| 60 | `/etc/init.d/ksd` | procd 拉起 `ksd run`：baseline → 尝试 full → 销毁 `ks_emergency` |
| — | ksd（常驻） | 每 5 秒查看端点集合，每 60 秒校验规则 |

如果 ksd 无法启动（二进制被删除、架构不对、SIGILL），`ks_emergency` 会一直生效：没有网络，但也不会泄漏。

> **TODO：** README 把 `openwrt/ksd-boot` 装成 `/etc/init.d/ksd`，而 BUILD.md 把 `ksd-boot` 和 `ksd` 当作两个独立的 init 脚本。本手册采用 BUILD.md 的方案。请核实并统一 README。

---

## 已知限制（未被保护的部分）

ksd **仅在其声明的威胁模型范围内**提供保护。

### 1. 特权本地攻击
Root 或 `CAP_NET_ADMIN` 可以伪造标记、修改规则或替换二进制。没有对二进制的运行时自校验。这是有意保留的限制。

### 2. Established/related 流量
已建立的连接按设计被信任。

### 3. 未知 DoH 服务器与 ECH
只阻断 `config doh` 中列出的 IP 的 tcp/443。任意 DoH、ECH 以及基于 CDN 的 DoH 都检测不到。这是设计限制，不是 bug。

### 4. 非标准端口上的 DNS/DoQ
明确阻断的有：53、853、8853（TCP+UDP）以及 udp/784（DoQ）。其余未打标记的流量由 fail-close 切断。

### 5. QUIC
当 `quic_block_enabled=1` 时，只阻断熬过 identification 阶段的 udp/443。默认不开启阻断。

### 6. ICMP 隐蔽信道
> **TODO：** 确认 Go 版本仍然放行用于 PMTU 发现的 ICMP 错误消息。如果是，保留原有表述（理论上可能存在隐蔽信道）。

### 7. 硬件/软件 offload
如果 fw4 开启了 flow offloading，同时 `forward_protect=1`，转发流量可能绕过 `killswitch_forward`。ksd 会在启动时和 `self-test` 中报告 **CRITICAL**，但不会自动修复。NIC 硬件 offload 同理。

> **TODO：** 确认是否像 shell v3.5 那样专门检查了硬件 flow offload（`flow_offloading_hw`）。

### 8. 没有 `gateway_mac` 的 ARP 防护
没有 `gateway_mac` 时只校验网关 IP，挡不住 ARP 欺骗（ksd 会给出警告）。

### 9. 对时间的依赖
防抖和定时器依赖 `time.Now()`。时钟不同步时逻辑会退化，因此请配置 `ntp_servers`（见下文）。

### 10. 内核被攻陷
如果内核被攻陷，计数器、校验器和恢复机制都可能不可信。

### 11. fw4
入站（WAN→LAN）过滤和 DNAT 是 fw4 的职责，不属于 ksd。

---

## 文件与路径

| 文件 | 用途 |
|------|------|
| `/usr/sbin/ksd` | 守护进程二进制 |
| `/etc/init.d/ksd` | procd init 脚本（START=60） |
| `/etc/init.d/ksd-boot` | 开机时安装 Layer 0（START=15） |
| `/etc/ksd/emergency.nft` | 静态应急规则集（权限 600） |
| `/etc/config/killswitch` | UCI 配置 |
| `/var/run/ksd-state.json` | 状态（tmpfs，缓存，可重新学习） |
| `CONFIG.md` | 所有选项说明 |
| `BUILD.md` | 编译与部署 |
| `docs/DESCRIPTION.zh.md` | 中文说明 |
| `ASSURANCE.md` | 保证契约（`TODO`：v4.0 的状态） |

相对 shell 版已删除：`/etc/killswitch_vps_elements.cache`、`/etc/killswitch/checksum.md5`、`/var/run/killswitch_mode`、`/var/run/killswitch_wan_if`。

### ksd 拥有的 nft 对象

```
inet killswitch_table
  ├── counters: dns_leak_drops, invalid_state_drops, quic_drops,
  │             allow_mark, allow_vps, allow_established, output_leak_drops,
  │             (+ _fwd variants), forward_leak_drops
  ├── sets:     vps_ipv4, ks_wan, ks_wan_gw   (dynamic)
  │             trusted_ifaces, doh_servers, ntp_bootstrap   (static)
  ├── chain killswitch_output   (filter/output/-10, policy drop)
  └── chain killswitch_forward  (filter/forward/-15, policy drop)
inet killswitch_mangle   → chain KS_POSTROUTING (-150, accept)
inet ks_emergency        → Layer 0, exists during startup
arp  killswitch_arp      → optional
```

以上对象全部归 ksd 所有，请勿手动往这些表里添加内容。

---

## NTP

务必配置 `ntp_servers`。在 full 模式下，sysntpd 发出的 udp/123 既不带标记也不在 VPS 集合里，如果不显式放行，就会被终止规则丢弃，时钟停止同步，而防抖逻辑依赖时间。

```bash
uci add_list killswitch.main.ntp_servers='129.6.15.28'
uci add_list killswitch.main.ntp_servers='132.163.97.1'
uci commit killswitch
/etc/init.d/ksd restart
```

---

## 故障排查

### 系统停在 baseline 而不是 full

```bash
logread -e ksd | grep -i "conntrack utility missing\|verification failed"
ksd status
```

可能的原因：
- 缺少 `conntrack` 工具；
- WAN 尚未就绪；
- 应用后校验失败。

如果 `full_fail` 达到 `full_fail_max`（默认 3），说明熔断器已触发：每 10 个校验周期重试一次（按 60 秒计约 10 分钟）。

### 校验失败

```bash
logread -e ksd | grep -i "verification failed"
ksd dry-run full
nft list chain inet killswitch_table killswitch_output
```

检查内容：链的 hook/priority/policy、模型中每个 `comment` 是否存在、**规则的相对顺序**、计数器是否存在、动态集合内容。请把内核中的实际规则集与 `dry-run` 的输出对比。

### VPS 集合为空 / `Source: DOWN`

```bash
nft list sets table inet passwall2
nft list set inet passwall2 psw2_vps
```

- 集合名必须与 `source_set` 一致（默认 `inet passwall2 psw2_vps`），类型必须是 `ipv4_addr`，否则 ksd 会拒绝使用。
- 检查 `static_vps` 和 `merge_static_vps`。
- 在 `strict_mode=1` 下，集合为空会阻断所有未打标记的流量。这是预期行为。
- 清空集合之前，ksd 会等待 `empty_source_grace_sec`（15 秒）。

### `output_leak_drops` 缓慢增长

最常见的原因是 NTP：配置 `ntp_servers`，执行 `ksd reset-counters`，然后观察。

### Forward 计数器一直为 0

很可能是 fw4 的 flow offloading 绕过了 `killswitch_forward`。在 `logread -e ksd` 和 `ksd self-test` 中查找 CRITICAL 消息。

### 从 LAN 侧 SSH 断了

`br-lan` 必须在 `allowed_iface` 里，否则 output 链会挡掉回包。通过串口或备用通道恢复：

```bash
ksd baseline          # 降级到 baseline
```

或者完全拆除：

```bash
service ksd stop
nft destroy table inet killswitch_table
nft destroy table inet killswitch_mangle
nft destroy table inet ks_emergency
nft destroy table arp killswitch_arp
```

通过配置实现同样效果：`uci set killswitch.main.force_stop=1`，然后 `service ksd stop`（守护进程会自行删除所有 nft 对象）。

---

## 设计理念

ksd v4.0 不是绝对的安全，而是一种**局部稳态机制**，它：
- 在选定的信任域内限制允许的轨迹空间；
- 用同一套因果规律（nftables）去增加另一类现象（泄漏）的难度；
- 把规则、校验和文档放在同一个模型里，使它们不会彼此漂移；
- 诚实地记录自身的边界。

---

## 何时查阅完整文档

- `CONFIG.md`：所有选项、默认值、ARP 段、DoH 段。
- `BUILD.md`：编译、架构、部署、紧急拆除。
- `ASSURANCE.md`：claims、evidence、assumptions、residual risks（`TODO`：对 Go 版是否仍然适用）。

---

## 草稿：待办事项（提交前删除）

1. 真实的 `ksd status` 和 `ksd self-test` 输出；补充 status 各字段的说明。
2. 配置如何重新读取：`reload` 还是只能 `restart`。
3. `ksd install` 做什么，使用它的那一段是否需要。
4. v4.0 的 ASSURANCE.md：与二进制 sha256 的绑定、Go 版本、文档版本。
5. 对照生产环境的 shell v3.5 核对优先级（-10/-15/-150）。
6. 确认 Go 版本中的 ICMP/PMTU 规则和 `flow_offloading_hw` 检查。
7. 统一 README 和 BUILD.md 中的 init 脚本方案（`ksd-boot` + `ksd`）。
8. 确认不再有锁文件。
9. 撰写时代码尚未编译，也未在路由器上测试（BUILD.md 第 8 节）：跑过 `go vet` 并完成首次运行后，删除或更新这条说明。

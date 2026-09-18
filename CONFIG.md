# 配置说明

配置文件：`/etc/config/killswitch`

所有选项都是可选的，不写就用默认值。示例见 `openwrt/killswitch.config`。

## 相比 shell 版的变化

| 老选项 | 新状态 | 说明 |
|---|---|---|
| `cache_ttl_hours` / `cache_hard_ttl_hours` | **删除** | 常驻进程每 5 秒直接读源集合，不再需要磁盘缓存和 TTL 推测 |
| `cache_file` | **删除** | 同上 |
| `empty_source_threshold`（次数） | 改为 `empty_source_grace_sec`（秒） | 时间语义比"连续 N 次"更好推理，且与轮询频率无关 |
| `monitoring_enabled` / `monitoring_interval_minutes` | 改为 `poll_interval_sec` / `verify_interval_sec` | 不再是 cron，是常驻循环 |
| `semantic_verify_enabled` | **删除** | 校验现在由规则模型自动生成，永远开启 |
| `reset_checksum_on_change` | **删除** | 完整性校验靠二进制分发，不做运行时自校验 |
| `monitor_after_stop` | **删除** | 常驻进程，没有"停止后继续监控"的概念 |
| `ntp_servers` | 现在 **full 模式也生效** | 修 P1-8：以前只有 baseline 有 NTP 放行规则 |
| `max_vps_elements` | 保留 | 超限现在会**拒绝整次更新**而不是静默截断 |
| `doh_block_enabled` + `config doh` | 保留 | 只能挡列出的 IP，任意 DoH/ECH 挡不住 |

新增：

| 选项 | 默认 | 说明 |
|---|---|---|
| `flush_on_narrow_only` | `1` | 只在端点集合**缩小**时清 conntrack。扩大时不清，避免打断 LAN 和 SSH |
| `merge_static_vps` | `0` | `1` = `static_vps` 永远并入放行集合（以前只在源不可用时兜底） |
| `full_fail_max` | `3` | full 模式连续失败几次后熔断（修 P0-3：防止 5 分钟一次的周期性全网断流） |
| `poll_interval_sec` | `5` | 端点集合轮询间隔 |
| `verify_interval_sec` | `60` | 规则完整性校验间隔 |

## main 段

### 身份

| 选项 | 默认 | 说明 |
|---|---|---|
| `table_name` | `killswitch_table` | inet 主表名 |
| `set_name` | `vps_ipv4` | VPN 端点集合名 |

### 端点来源

```uci
option source_set 'inet passwall2 psw2_vps'
```

格式：`<family> <table> <set>`。用下面的命令确认 passwall2 实际用的集合名：

```bash
nft list sets table inet passwall2
```

ksd 会检查集合类型是 `ipv4_addr`，不是就拒绝使用并报错。

### 网络

| 选项 | 默认 | 说明 |
|---|---|---|
| `wan_interface` | `wan` | netifd 逻辑接口名（用于 ubus） |
| `wan_device` | 空 | 显式指定设备（如 `eth1`），跳过 ubus 探测 |
| `allowed_iface` | `lo` `br-lan` | 受信任出接口，放行。**写错会把自己的 SSH 挡掉** |
| `allowed_mark` | `0x50535732` `0x000000ff` | passwall2 打的 mark |

> `br-lan` 一定要在 `allowed_iface` 里，否则从 LAN 侧 SSH 进路由器会被 output 链终止规则 drop。

### 功能开关

| 选项 | 默认 | 说明 |
|---|---|---|
| `strict_mode` | `1` | 端点集合空 = 挡掉所有非 mark 流量 |
| `dns_block_enabled` | `1` | 无条件 drop 53/853/8853（TCP+UDP） |
| `doq_block_enabled` | `1` | 无条件 drop udp 784 |
| `doh_block_enabled` | `1` | 只对列出的 IP 生效 |
| `quic_block_enabled` | `0` | 只挡活过 identification 的 udp 443 |
| `ipv6_block_enabled` | `1` | 关闭时 IPv6 仍 fail-close（只放行带 mark 的） |
| `mss_clamp_enabled` | `1` | postrouting 改 MSS |
| `ttl_set_enabled` | `0` | postrouting 改 TTL=64 |
| `forward_protect` | `1` | LAN 客户端保护 |

> `forward_protect=1` 时如果 fw4 开了 flow offloading，转发流会绕过 `killswitch_forward`。ksd 启动和自检都会报 CRITICAL。

### 优先级

| 选项 | 默认 |
|---|---|
| `filter_priority` | `-10` |
| `forward_filter_priority` | `-15` |
| `mangle_priority` | `-150` |

fw4 用 0，所以负值 = 我们先进。

### 时序

| 选项 | 默认 | 说明 |
|---|---|---|
| `poll_interval_sec` | `5` | 端点集合轮询 |
| `verify_interval_sec` | `60` | 规则完整性校验 |
| `empty_source_grace_sec` | `15` | 源不健康多久后才清空集合（防抖） |
| `full_fail_max` | `3` | 熔断阈值 |

### 限制与行为

| 选项 | 默认 | 说明 |
|---|---|---|
| `max_vps_elements` | `5000` | 超限拒绝更新 |
| `flush_on_narrow_only` | `1` | 见上表 |
| `merge_static_vps` | `0` | 见上表 |
| `force_stop` | `0` | `1` = 停止时删除所有 nft 对象 |

### 诊断

| 选项 | 默认 | 说明 |
|---|---|---|
| `log_suspicious` | `1` | 计数器增量告警 |
| `log_drops` | `0` | 开的话终止规则会拆成"计数"+"限速日志"两条 |
| `log_file` | 空 | 额外文件日志，超过 `log_max_kb` 轮转一代 |
| `log_max_kb` | `512` | |

日志主要走 stderr，procd 会收进 syslog，所以 `logread -e ksd` 就能看。

### NTP

```uci
list ntp_servers '129.6.15.28'
list ntp_servers '132.163.97.1'
```

**建议务必配置。** full 模式下 sysntpd 的 udp/123 出站既不带 mark 也不在端点集合里，不放行就会被终止规则 drop，时钟停止同步 —— 而整套缓存/防抖逻辑都依赖 `time.Now()`。

## doh 段

```uci
config doh
    list server '1.1.1.1'
    list server '8.8.8.8'
```

只挡这列出的 IP 的 tcp/443。**任意 DoH、ECH、以及基于 CDN 的 DoH 都挡不住** —— 这是设计限制，不是 bug。

## arp 段（可选）

```uci
# main 段里：
option arp_protection '1'

config arp
    option interface   'eth1'
    list   gateway     '192.168.0.1'
    list   gateway_mac 'aa:bb:cc:dd:ee:ff'
```

注意事项：

- 必须同时给 `main` 段的 `arp_protection` 和独立的 `config arp` 段
- 不填 `gateway_mac` 时只校验网关 IP，**挡不住 ARP 欺骗**，ksd 会打警告
- 链策略是 `accept`，只在 WAN 口加显式 drop，不会动 LAN 的 ARP
- 接口不能是 `allowed_iface` 里的任何一个，否则拒绝安装

## 运行时状态

`/var/run/ksd-state.json`（tmpfs，重启即失）。内容是纯缓存，丢了会在一个轮询周期内重新学习：

```json
{
  "mode": "full",
  "wan": "eth1",
  "gw": "192.168.0.1",
  "last_vps": ["1.2.3.4", "5.6.7.8"],
  "counters": { "dns_leak_drops": 9 },
  "counters_init": true,
  "full_fail": 0,
  "source_down": false
}
```

## nft 对象总览

ksd 拥有这些对象，其它东西别往里塞：

```
inet killswitch_table
  ├── counters: dns_leak_drops, invalid_state_drops, quic_drops,
  │             allow_mark, allow_vps, allow_established, output_leak_drops,
  │             + 上述各项的 _fwd 版本, forward_leak_drops
  ├── sets:     vps_ipv4        (动态：VPN 端点)
  │             ks_wan          (动态：当前 WAN 设备)
  │             ks_wan_gw       (动态：255.255.255.255 + 默认网关)
  │             trusted_ifaces  (静态)
  │             doh_servers     (静态，可选)
  │             ntp_bootstrap   (静态，可选)
  ├── chain killswitch_output   (filter/output/-10/drop)
  └── chain killswitch_forward  (filter/forward/-15/drop)

inet killswitch_mangle
  └── chain KS_POSTROUTING      (filter/postrouting/-150/accept)

inet ks_emergency               (Layer 0，启动期临时存在)

arp  killswitch_arp             (可选)
```

`ks_wan` / `ks_wan_gw` 是关键设计：WAN 变了只更新集合元素，**不重建规则集**。这也让多 WAN 变成"加个元素"而不是"生成 N 条规则"。

## 规则即契约

每条规则都带 `comment`，校验就是拿模型里的 comment 序列去内核里比对：

- 链的 hook / priority / policy 一致
- 模型里每条 comment 都存在
- **相对顺序完全一致**（这一条覆盖了原来 jq 里所有 "A 必须在 B 之前" 的因果校验）
- 计数器对象都在
- 动态集合内容符合预期

所以你改规则时只改一处（模型），校验自动跟着变 —— 不存在"生成器和校验器各写一遍然后漂移"的问题。

## 排查线索

| 现象 | 看什么 |
|---|---|
| `status` 里 `Source` 显示 `DOWN` | passwall2 表没了；ksd 会清 conntrack 并计入防抖 |
| 一直停在 baseline | `logread -e ksd` 找 `conntrack utility missing` 或 `verification failed` |
| `full_fail` 涨到 3 | 熔断了；每 10 个校验周期半开重试一次 |
| Forward 侧计数器全 0 | 大概率 fw4 flow offloading 旁路了 |
| `output_leak_drops` 缓慢增长 | 多半是 NTP（配 `ntp_servers`）；`ksd reset-counters` 后观察 |

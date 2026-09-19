# ksd — Go 版 Killswitch 守护进程

已生成完整项目，打包在 `ksd.zip`。

---

## 编译（你的设备）

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ksd .
```

零依赖（`go.mod` 无 require），产物约 2.5MB，512MB RAM 无压力。

---

## 代码结构（12 个 .go，约 2900 行）

| 文件 | 职责 |
|---|---|
| `config.go` | 类型化配置 + 校验，替代约 250 行 `config_get` + 正则 |
| `uci.go` | 直接解析 UCI 文件，不 exec `uci` |
| `nft.go` | `nft` 封装 + 极简 JSON 模型（按需解码，schema 变了也不崩） |
| `ruleset.go` | 声明式规则模型 + 渲染 |
| `verify.go` | 校验**由模型推导** |
| `wan.go` | ubus + `ip route` 探测 |
| `state.go` | 单文件 JSON 状态，替代散落十几个 `/var/run` 文件 |
| `install.go` / `daemon.go` | 安装与常驻循环 |
| `main.go` | CLI |

---

## 三个关键设计

### 1. 规则即契约

每条规则都带 `comment`，校验就是拿模型里的 comment 序列去内核比对：

- 链参数一致
- comment 全在
- **相对顺序完全一致**
- 计数器在
- 动态集合内容对得上

相对顺序这一条就覆盖了原 jq 里所有「A 必须在 B 之前」的因果断言。

改规则只改一处，校验自动跟着变 —— 原来那种「生成器和校验器各写一遍然后漂移」的问题不存在了。

### 2. WAN 和网关进集合

`ks_wan` / `ks_wan_gw` 是 nft set，规则文本里只写 `oifname @ks_wan`。

WAN 变了只更新集合元素，不重建规则集、不清 conntrack。多 WAN 也从「生成 N 条规则」变成「加个元素」。

### 3. 分层 fail-close

| START | 组件 | 行为 |
|---|---|---|
| 15 | `ksd-boot` | 装 `/etc/ksd/emergency.nft`（静态文件，零逻辑零依赖） |
| 60 | `ksd run` | 装 baseline → 试 full，成功后销毁 emergency |
| — | 兜底 | ksd 二进制被删 / 架构不对 SIGILL 起不来 → emergency 一直生效，设备没网但**不漏** |

---

## 已落地的评审修复

- **P0-1** — 常驻进程每 5s 直接读源，不需要 cron
- **P0-2** — baseline 仍走 `nft -f`，失败才退化
- **P0-3** — `full_fail_max` 熔断
- **P1-4 / P1-5 / P1-6** — 只在集合**缩小**时清 conntrack，失败直接降级
- **P1-7** — 计数与限速日志拆两条
- **P1-8** — NTP 在 full 模式也放行
- **P1-10** — flow offload 冲突在启动期报 CRITICAL
- **P2-1 ～ P2-7** — 全部

以下选项**删掉了**：

```
cache_ttl_hours
empty_source_threshold   （计数语义）
monitoring_*
semantic_verify_enabled
```

常驻进程下它们补偿的问题本身消失了。`CONFIG.md` 里有完整的新旧对照表。

---

## 一点必须提醒

### `nft flush table` 的隐患（shell 版就存在）

`nft flush table` **只清规则**，链、集合、计数器全部保留（nftables 官方文档明确写了）。

所以重复应用时 `add set ... elements = {...}` 是 no-op。

ksd 因此**不依赖**文件里内联的 elements —— WAN / 网关 / 端点内容一律由 `flush set` + `add element` 单独下发（见 `applyDynamicSets`）。

`BUILD.md` 第 8 节记了这一点和兜底改法。

---

## 首次上线顺序

按 `BUILD.md` 第 3 节走：

1. `ksd dry-run` 看规则
2. `ksd baseline` 确认没把自己锁在外面
3. 最后才 `service ksd start`

全程最好留一条串口或不经过 killswitch 的管理通道。

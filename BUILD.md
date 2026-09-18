# 编译与部署

目标设备：**Routerich AX3000 v1**（MT7981，aarch64_cortex-a53，512MB RAM）
系统：**OpenWrt 25.12.5, r33051-f5dae5ece4**

## 0. 为什么这套代码零依赖

`go.mod` 里没有任何 `require`。原因很实际：

- 不需要 module proxy，任何网络环境都能编译
- `CGO_ENABLED=0` 天然静态链接，不用管 musl/glibc
- 二进制 ~2.5MB（strip 后），512MB RAM 的设备毫无压力

## 1. 在开发机上编译

装 Go 1.22+（任意平台），然后：

```bash
cd ksd
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o ksd .
```

产物检查：

```bash
file ksd
# ksd: ELF 64-bit LSB executable, ARM aarch64, statically linked, stripped
ls -la ksd     # 约 2.3~2.8 MB
```

### 架构对照表

| 设备 | GOARCH | 备注 |
|---|---|---|
| MT7981 / MT7986 / 其它 ARM64 路由 | `arm64` | **AX3000 v1 用这个** |
| MT7621 / MT7620（MIPS 大端） | `mips` + `GOMIPS=softfloat` | |
| MT7628 / MT76x8（MIPS 小端） | `mipsle` + `GOMIPS=softfloat` | |
| x86_64 软路由 | `amd64` | |
| BCM4908 / 其它 ARMv7 | `arm` + `GOARM=7` | |

> MIPS 平台**必须**带 `GOMIPS=softfloat`，否则运行时 `Illegal instruction` —— 编译期不报错，只能上机实测。ARM64 无此问题。

### 可选：再压一点

```bash
upx --best --lzma ksd    # 2.5MB -> 约 900KB
```

UPX 会让首次启动多花几十毫秒解压。512MB RAM 的设备不建议（省这点空间没意义，反而增加复杂度）。

## 2. 装到路由器

```bash
scp ksd root@192.168.1.1:/usr/sbin/ksd
ssh root@192.168.1.1 '
  chmod 755 /usr/sbin/ksd
  mkdir -p /etc/ksd
'
```

然后放三个文件：

| 源文件 | 目标路径 | 权限 |
|---|---|---|
| `openwrt/ksd-boot` | `/etc/init.d/ksd-boot` | 755 |
| `openwrt/ksd` | `/etc/init.d/ksd` | 755 |
| `openwrt/emergency.nft` | `/etc/ksd/emergency.nft` | 600 |
| `openwrt/killswitch.config` | `/etc/config/killswitch` | 644 |

```bash
scp openwrt/ksd-boot  root@192.168.1.1:/etc/init.d/ksd-boot
scp openwrt/ksd       root@192.168.1.1:/etc/init.d/ksd
scp openwrt/emergency.nft root@192.168.1.1:/etc/ksd/emergency.nft
scp openwrt/killswitch.config root@192.168.1.1:/etc/config/killswitch

ssh root@192.168.1.1 '
  chmod 755 /etc/init.d/ksd-boot /etc/init.d/ksd
  chmod 600 /etc/ksd/emergency.nft
  /etc/init.d/ksd-boot enable
  /etc/init.d/ksd enable
'
```

## 3. 首次启动（强烈建议按这个顺序）

别直接 `service ksd start`，先看清楚要装什么：

```bash
# 1) 看生成的规则（不应用）
ksd dry-run baseline
ksd dry-run full

# 2) 只装应急 baseline，确认没把自己锁在外面
ksd baseline
ksd status

# 3) 确认 SSH 还能用、DHCP 还活着，再让它进 full
service ksd start
sleep 5
ksd status
ksd self-test
```

**建议全程用串口或保留一条不经过 killswitch 的管理通道。** 如果 `br-lan` 不在 `allowed_iface` 里，LAN 侧的 SSH 会被 output 链的终止 drop 挡掉。

## 4. 验证

```bash
logread -e ksd | tail -40          # 看决策日志
nft list table inet killswitch_table
nft list set inet killswitch_table vps_ipv4 | head
nft list counters table inet killswitch_table
ksd self-test
```

正常时你应该看到：

```
[OK]   table inet killswitch_table present
[OK]   live ruleset matches model (<nil>)
[OK]   VPS set vps_ipv4 present
[OK]   conntrack utility available
[OK]   DNS query to 1.1.1.1 blocked (expected)
Result: PASS
```

## 5. 开机时序

| START | 组件 | 干什么 |
|---|---|---|
| 15 | `/etc/init.d/ksd-boot` | 装 `ks_emergency`（静态文件，零依赖） |
| 20 | netifd | WAN 起来 |
| 60 | `/etc/init.d/ksd` | procd 拉起 `ksd run`：装 baseline → 试 full |
| — | ksd（常驻） | 每 5s 看端点集合，每 60s 校验规则 |

`ksd` 装好自己的规则后立刻 `destroy table inet ks_emergency`。如果 `ksd` 因为任何原因（二进制被删、架构不对 SIGILL）起不来，`ks_emergency` 一直生效 —— 设备没网，但**不漏**。

## 6. 故障处理

### 完全拆掉（包括 Layer 0）

```bash
service ksd stop
nft destroy table inet killswitch_table
nft destroy table inet killswitch_mangle
nft destroy table inet ks_emergency
nft destroy table arp  killswitch_arp
```

或者：

```bash
uci set killswitch.main.force_stop=1
uci commit killswitch
service ksd stop        # ksd 收到 SIGTERM 后自行 RemoveAll
```

### 只想降级到 baseline

```bash
ksd baseline
```

### 计数器归零

```bash
ksd reset-counters
```

## 7. 交叉编译脚本（可选）

`build.sh`：

```bash
#!/bin/sh
set -e
: "${GOARCH:=arm64}"
OUT="ksd-${GOARCH}"
env CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" ${GOMIPS:+GOMIPS=$GOMIPS} \
  ${GOARM:+GOARM=$GOARM} \
  go build -trimpath -ldflags="-s -w" -o "$OUT" .
echo "built $OUT ($(du -h "$OUT" | cut -f1))"
```

## 8. 已知未验证项

这份代码**没有在本机编译过**（撰写环境无 Go 工具链）。首次构建请先跑：

```bash
go vet ./...
go build ./...
```

若 `go vet` 报 unused variable / import，按提示删掉即可，不影响逻辑。

另外几处需要你在真机上确认：

1. `arp saddr ether != @set` 这类表达式在你的 nft 版本上是否可用（ARP 表默认关闭，不放心就保持 `arp_protection=0`）
2. passwall2 的端点集合名是否真的是 `psw2_vps` —— 用 `nft list sets table inet passwall2` 确认，不对就改 `source_set`
3. 规则文件里用的是 `add table` / `add chain` / `add set` / `add counter`。`nft flush table` **只清规则**，链、集合、计数器都会留下来，所以重复应用时这些 `add` 必须是幂等的 no-op（原来的 shell 脚本也完全依赖这一点，并且实测可重复 reload）。ksd 因此**不依赖**文件里内联的 `elements`，WAN/网关/端点集合的内容一律由 `flush set` + `add element` 单独下发。如果你在真机上看到 `File exists`，把 `Render()` 里的 `add set` 改成先 `destroy set` 再 `add set` 即可。

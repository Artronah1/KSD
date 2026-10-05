# ksd — демон Killswitch для OpenWrt
<p align="left">
  <a href="./README.md"><img src="https://img.shields.io/badge/lang-Русский-brightgreen.svg" alt="Русский"></a>
  <a href="./README.en.md"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
  <a href="./README.zh.md"><img src="https://img.shields.io/badge/lang-中文-blue.svg" alt="中文"></a>
</p>
<p align="center">
  <img src="Project.png" alt="KSD" width="350">
</p>
<p align="center">
  <img src="https://img.shields.io/badge/Go-1.27.1-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/Platform-OpenWrt-orange" alt="OpenWrt">
  <img src="https://img.shields.io/badge/Status-v1.3.x-blue" alt="Status">
</p>
Демон на Go, реализующий fail-close killswitch для OpenWrt поверх nftables.
Предназначен для использования с Passwall2: весь немаркированный трафик
блокируется, если он не идёт к разрешённым VPS-адресам.

## Возможности

- Атомарное применение правил nftables (nft transaction)
- Автоматический rollback в baseline при ошибках
- Блокировка DNS / DoT / DoQ / DoH утечек
- Опциональная блокировка QUIC (UDP/443)
- Защита LAN-клиентов через forward-хук
- ARP-защита от спуфинга
- MSS clamp и опциональный TTL set
- Периодическая верификация правил в ядре
- Нулевые внешние зависимости (CGO_ENABLED=0, статическая сборка)

### Скачивание

Выбери файл под архитектуру роутера (`uname -m`):

| `uname -m` | Файл |
|---|---|
| `aarch64` | `ksd-arm64` |
| `x86_64` | `ksd-amd64` |
| `mips` | `ksd-mips` |
| `mipsel` | `ksd-mipsle` |

Скачай со страницы релиза:
https://github.com/Artronah1/KSD/releases/latest

Проверь sha256:
    sha256sum ksd-arm64

## Сборка

    go build -o ksd ./...

Кросс-компиляция под OpenWrt (пример для ARM64):

    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ksd-arm64 ./...


## Установка

> ⚠ **Держи UART или вторую SSH-сессию открытой.** Если ksd заблокирует доступ — восстановишь оттуда.

1. Скачать бинарник под архитектуру роутера (`uname -m`):

       wget https://github.com/Artronah1/KSD/releases/latest/download/ksd-arm64
       sha256sum ksd-arm64

   Хеш должен совпасть с `sha256` на странице релиза.

2. Скопировать файлы на роутер:

       scp ksd-arm64 root@192.168.1.1:/tmp/ksd
       scp openwrt/killswitch.config root@192.168.1.1:/etc/config/killswitch
       scp openwrt/ksd root@192.168.1.1:/etc/init.d/ksd
       scp openwrt/ksd-boot root@192.168.1.1:/etc/init.d/ksd-boot
       scp openwrt/emergency.nft root@192.168.1.1:/etc/ksd/emergency.nft

3. Установить на роутере:

       ssh root@192.168.1.1

       cp /tmp/ksd /usr/sbin/ksd
       chmod +x /usr/sbin/ksd /etc/init.d/ksd /etc/init.d/ksd-boot
       /etc/init.d/ksd-boot enable
       /etc/init.d/ksd enable

4. Проверить конфиг `/etc/config/killswitch`:

       nano /etc/config/killswitch

   Убедиться, что корректны: `source_set`, `wan_interface` / `wan_device`,
   `allowed_iface`, `arp_protection` и `arp_gateway_mac`.

5. Первый запуск:

       /usr/sbin/ksd install -config /etc/config/killswitch

6. Запустить сервис:

       /etc/init.d/ksd start

7. Проверка:

       /usr/sbin/ksd status -config /etc/config/killswitch
       /usr/sbin/ksd self-test -config /etc/config/killswitch
       
## Автоматическая установка

Универсальный installer: сам определяет архитектуру, WAN, LAN-бриджи,
шлюз и его MAC, источник VPS, генерирует `/etc/config/killswitch`,
скачивает подходящий бинарник и устанавливает сервис.

**На роутере:**

    wget -O /tmp/install.sh https://raw.githubusercontent.com/Artronah1/KSD/main/scripts/install.sh
    sh /tmp/install.sh

**Опции:**

    sh /tmp/install.sh -v v1.1.4        # конкретная версия
    sh /tmp/install.sh -f /tmp/ksd-arm64  # локальный бинарник

**Если конфиг `/etc/config/killswitch` уже существует** — installer его
сохранит и не тронет. Проверь значения `source_set`, `wan_device`,
`arp_gateway` **вручную**.

**После установки** — `restart`:

    /etc/init.d/ksd restart

## Документация

- [CONFIG.md](CONFIG.md) — описание всех UCI-опций
- [BUILD.md](BUILD.md) — детали сборки
- [docs/OPERATOR.md](docs/OPERATOR.md) — операторское руководство
- [docs/DESCRIPTION.md](docs/DESCRIPTION.md) — описание проекта

## Лицензия

木兰公共许可证第 2 版 (Mulan PubL v2) — подробности в [LICENSE](LICENSE).

**Copyleft** с сетевой оговоркой: производные работы, включая
предоставление сервисов через сеть, должны распространяться под той же
лицензией с открытым исходным кодом.

## Статус

- v3.5 (shell) — reference implementation, frozen 2026-08-30
- v1.3.x (Go) — current stable release

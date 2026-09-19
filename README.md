# KSD — Killswitch daemon for OpenWrt
<p align="left">
  <a href="./README.md"><img src="https://img.shields.io/badge/lang-Русский-brightgreen.svg" alt="Русский"></a>
  <a href="./README.en.md"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
  <a href="./README.zh.md"><img src="https://img.shields.io/badge/lang-中文-blue.svg" alt="中文"></a>
</p>
<p align="center">
  <img src="Project.png" alt="KSD" width="350">
</p>
<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/Platform-OpenWrt-orange" alt="OpenWrt">
  <img src="https://img.shields.io/badge/Status-v4.0--dev-yellow" alt="Status">
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

## Сборка

    go build -o ksd ./...

Кросс-компиляция под OpenWrt (пример для ARM64):

    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ksd-arm64 ./...

## Установка

1. Скопировать бинарник на роутер:
       scp ksd root@192.168.1.1:/usr/sbin/ksd

2. Установить UCI-конфиг:
       scp openwrt/killswitch.config root@192.168.1.1:/etc/config/killswitch

3. Установить init-скрипт:
       scp openwrt/ksd-boot root@192.168.1.1:/etc/init.d/ksd
       ssh root@192.168.1.1 'chmod +x /etc/init.d/ksd && /etc/init.d/ksd enable'

4. Запустить:
       ssh root@192.168.1.1 '/etc/init.d/ksd start'

## Конфигуратор

Интерактивный TUI-конфигуратор для UCI:

    scripts/ksd-configurator

Позволяет управлять всеми опциями `/etc/config/killswitch` без ручного `uci set`.
После изменений используйте пункт «Применить и перезапустить».

## Документация

- [CONFIG.md](CONFIG.md) — описание всех UCI-опций
- [BUILD.md](BUILD.md) — детали сборки
- [docs/OPERATOR.md](docs/OPERATOR.md) — операторское руководство
- [docs/DESCRIPTION.zh.md](docs/DESCRIPTION.zh.md) — описание на китайском

## Лицензия

Проект распространяется без лицензии (all rights reserved).
Исходный код является частной разработкой.

## Статус

- v3.5 (shell) — reference implementation, frozen 2026-08-30
- v4.0 (Go) — active development

# ksd (Killswitch v4.0, Go) — Операторское руководство

**Версия:** v4.0-dev  
**Дата freeze:** не заморожен (reference-реализация: shell v3.5, frozen 2026-08-30)  
**SHA-256 бинарника:** `TODO` — считать после сборки: `sha256sum ksd`  
**Цель:** Routerich AX3000 v1 (MT7981, aarch64), OpenWrt 25.12.5

> **Статус черновика.** Места, где нужны реальные данные с роутера или твоё решение, помечены `TODO`. Список в конце файла — удали его перед коммитом.

---

## Что это такое

ksd — резидентный демон на Go, реализующий fail-close firewall для OpenWrt поверх nftables. Весь исходящий трафик роутера либо:

- помечен маркой Passwall2 (`allowed_mark`: `0x50535732`, `0x000000ff`);
- идёт к разрешённым VPS-адресам (динамический set `vps_ipv4`);
- идёт через доверенный интерфейс (`allowed_iface`: `lo`, `br-lan`);
- является частью установленного соединения (established/related);
- или блокируется.

**Ключевые свойства:**

- Атомарное применение правил (одна nft-транзакция).
- Откат в baseline при ошибках; `full_fail_max` (по умолчанию 3) — защита от циклических обрывов связи.
- Верификация выводится из модели правил: каждое правило имеет `comment`, ядро сверяется с моделью (параметры цепей, наличие и **порядок** правил, счётчики, содержимое динамических set'ов). Отдельного «валидатора» больше нет, поэтому он не может разойтись с генератором.
- Защита от DNS/DoT/DoQ/DoH-утечек; QUIC (udp/443) опционально.
- Защита LAN-клиентов через forward-хук.
- Опциональная ARP-защита WAN-порта.
- Слоистый fail-close: `ks_emergency` (Layer 0) → baseline → full.
- Нулевые внешние зависимости, статическая сборка (`CGO_ENABLED=0`).

---

## Что изменилось по сравнению с shell v3.5

| Было (shell) | Стало (Go) |
|---|---|
| cron-мониторинг (`monitoring_*`) | резидентный цикл: `poll_interval_sec` = 5 (набор эндпоинтов), `verify_interval_sec` = 60 (целостность правил) |
| кэш VPS-set на диске, `cache_ttl_hours`, `cache_file` | удалено: демон каждые 5 с читает источник напрямую |
| `empty_source_threshold` (N раз подряд) | `empty_source_grace_sec` = 15 (секунды, не зависит от частоты опроса) |
| `semantic_verify_enabled` | удалено: верификация всегда включена, выводится из модели |
| `reset_checksum_on_change`, `checksum.md5` | удалено: целостность обеспечивается дистрибуцией бинарника, runtime-самопроверки нет |
| `monitor_after_stop` | удалено: демон резидентный |
| `ntp_servers` только в baseline | теперь работает и в **full** |
| `max_vps_elements`: тихое усечение | превышение **отклоняет всё обновление** |
| conntrack flush при каждом изменении | по умолчанию только когда набор эндпоинтов **сужается** (`flush_on_narrow_only=1`) |
| `dry_run`, `test`, `logread -e killswitch` | `dry-run`, `self-test`, `logread -e ksd` |
| десяток файлов в `/var/run/` | один `/var/run/ksd-state.json` (чистый кэш, восстанавливается за один цикл опроса) |
| WAN-интерфейс и шлюз зашиты в правила | вынесены в set'ы `ks_wan` / `ks_wan_gw`: смена WAN обновляет элементы и не пересобирает правила |
| priority `filter`/`forward`/`mangle` | по умолчанию `-10` / `-15` / `-150` (fw4 использует 0, значит ksd срабатывает раньше) |

> **TODO:** сверить, какие приоритеты использовала shell v3.5 в production, и при необходимости подправить строку про priority. Также подтвердить, что lock-файл (`/var/run/killswitch.lock`) больше не нужен.

---

## Быстрая диагностика

### Текущее состояние

```bash
ksd status
```

Из state-файла и статуса нужно смотреть:

- **Mode:** `full` — полная защита, `baseline` — минимальная.
- **Source:** `DOWN` — исходной таблицы Passwall2 нет (ksd почистит conntrack и учтёт это в debounce).
- **full_fail:** счётчик неудач перехода в full. Достиг `full_fail_max` — сработал предохранитель.
- **Counters:** сколько пакетов заблокировано (`output_leak_drops`, `dns_leak_drops` и т. д.).

> **TODO:** вставить реальный вывод `ksd status` и расписать поля по факту. Сейчас описание построено по структуре `/var/run/ksd-state.json`, а не по выводу команды.

### Быстрая проверка

```bash
ksd self-test
```

Ожидаемый вывод (пример из BUILD.md, проверить на реальной сборке):

```
[OK]   table inet killswitch_table present
[OK]   live ruleset matches model (<nil>)
[OK]   VPS set vps_ipv4 present
[OK]   conntrack utility available
[OK]   DNS query to 1.1.1.1 blocked (expected)
Result: PASS
```

### Генерация правил без применения

```bash
ksd dry-run full        # показать full ruleset
ksd dry-run baseline    # показать baseline ruleset
```

### Логи и счётчики

```bash
logread -e ksd | tail -n 50
nft list counters table inet killswitch_table
nft list set inet killswitch_table vps_ipv4 | head
```

Логи идут в stderr, procd отправляет их в syslog. Дополнительно можно включить файл: `log_file` и `log_max_kb` (по умолчанию 512, ротация одной генерации).

---

## Что делать при изменениях

### 1. Изменение конфигурации (UCI)

```bash
uci set killswitch.main.<параметр>=<значение>
uci commit killswitch
/etc/init.d/ksd restart
```

> **TODO:** выяснить, перечитывает ли демон конфиг по `reload` (SIGHUP / procd-триггер) или только после `restart`. Пока в руководстве используется `restart` как заведомо рабочий вариант. Сам `ksd` парсит UCI-файл напрямую (не через `uci`), поэтому `uci commit` обязателен: без него на диске ничего не изменится.

Полное описание всех опций и значений по умолчанию: `CONFIG.md`.

### 2. Изменение кода (сборка новой версии)

```bash
# 1. Правишь исходник
cd ~/ksd
nano daemon.go

# 2. Проверка
go vet ./...
go build ./...

# 3. Кросс-сборка
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o ksd-arm64 .
sha256sum ksd-arm64

# 4. Копирование на роутер
scp ksd-arm64 root@192.168.1.1:/tmp/ksd-new

# 5. На роутере: замена бинарника
cp /tmp/ksd-new /usr/sbin/ksd.new
chmod 755 /usr/sbin/ksd.new
mv /usr/sbin/ksd.new /usr/sbin/ksd

# 6. Проверка до применения
ksd dry-run full
ksd self-test

# 7. Перезапуск
/etc/init.d/ksd restart
ksd status
```

> **Почему `mv`, а не прямой `cp` поверх `/usr/sbin/ksd`:** пока демон работает, запись в его исполняемый файл упадёт с `Text file busy`. Сначала копируем рядом, потом атомарно переименовываем (rename в пределах одной ФС безопасен при работающем процессе).

При остановке демона правила остаются в ядре (`force_stop=0` по умолчанию), то есть fail-close сохраняется даже во время замены бинарника.

Если нужна полная переустановка правил:

```bash
/etc/init.d/ksd stop
/usr/sbin/ksd install -config /etc/config/killswitch
/etc/init.d/ksd start
```

> **TODO:** что именно делает `ksd install`? В BUILD.md этой команды нет (там `run`, `dry-run`, `baseline`, `status`, `self-test`, `reset-counters`). Если она не нужна, убрать этот блок.

> **TODO (контракт):** остаётся ли привязка версии к sha256 в `ASSURANCE.md`? Если да, после шага 3 нужно обновить раздел «Artifact Identity» (версия, хеш, версия Go, флаги сборки `-trimpath -ldflags="-s -w"`) и закоммитить вместе с кодом.

### 3. Верификация правил (семантическая проверка)

Переключателя больше нет: проверка всегда включена. Чтобы изменить правила, правь **только модель** (`ruleset.go`). Генерация и проверка идут из одного источника и поменяются вместе.

---

## Первый запуск и загрузка

Не запускай сразу `service ksd start`. Порядок:

```bash
ksd dry-run baseline
ksd dry-run full
ksd baseline          # поставить только baseline, убедиться что SSH/DHCP живы
ksd status
service ksd start
sleep 5
ksd status
ksd self-test
```

Всё это лучше делать с запасной управляющей связью (серийный порт или канал в обход killswitch). Если `br-lan` не в `allowed_iface`, SSH из LAN блокируется output-цепочкой.

### Порядок загрузки

| START | Компонент | Что делает |
|---|---|---|
| 15 | `/etc/init.d/ksd-boot` | ставит `ks_emergency` (статический файл, без логики и зависимостей) |
| 20 | netifd | поднимается WAN |
| 60 | `/etc/init.d/ksd` | procd запускает `ksd run`: baseline → попытка full → удаление `ks_emergency` |
| — | ksd (резидент) | каждые 5 с проверяет набор эндпоинтов, каждые 60 с проверяет правила |

Если ksd не стартует (бинарник удалён, не та архитектура, SIGILL), `ks_emergency` остаётся активным: связи нет, но и утечек нет.

> **TODO:** в README `openwrt/ksd-boot` ставится как `/etc/init.d/ksd`, а в BUILD.md `ksd-boot` и `ksd` это два разных init-скрипта. Здесь использована схема из BUILD.md. Проверить и выровнять README.

---

## Известные ограничения (что НЕ защищено)

ksd защищает **только в рамках заявленной модели угроз**.

### 1. Привилегированные локальные атаки
Root или `CAP_NET_ADMIN` может подделать mark, изменить правила или подменить бинарник. Runtime-самопроверки бинарника нет. Это осознанное ограничение.

### 2. Established/related трафик
Установленные соединения доверяются по дизайну.

### 3. Неизвестные DoH-серверы и ECH
Блокируется только tcp/443 к IP из `config doh`. Произвольный DoH, ECH и DoH за CDN не детектируются. Это ограничение дизайна, не баг.

### 4. DNS/DoQ на нестандартных портах
Явно блокируются 53, 853, 8853 (TCP+UDP) и udp/784 (DoQ). Всё остальное немаркированное режет fail-close.

### 5. QUIC
При `quic_block_enabled=1` блокируется только udp/443, прошедший стадию identification. По умолчанию блокировка выключена.

### 6. ICMP covert channels
> **TODO:** подтвердить, что в Go-версии ICMP error messages по-прежнему разрешены для PMTU discovery. Если да, оставить прежнюю формулировку (теоретически возможны скрытые каналы).

### 7. Hardware/software offload
Если в fw4 включён flow offloading, а `forward_protect=1`, forward-трафик может обходить `killswitch_forward`. ksd сообщает об этом как **CRITICAL** при старте и в `self-test`, но не исправляет. Аппаратный offload NIC покрывается тем же образом.

> **TODO:** уточнить, проверяется ли именно hardware flow offload (`flow_offloading_hw`), как в shell v3.5.

### 8. ARP-защита без `gateway_mac`
Без `gateway_mac` проверяется только IP шлюза, ARP-спуфинг не блокируется (ksd предупредит).

### 9. Зависимость от времени
Debounce и таймеры опираются на `time.Now()`. Без синхронизации часов логика деградирует, поэтому `ntp_servers` стоит задать (см. ниже).

### 10. Компрометация ядра
Если ядро скомпрометировано, счётчики, верификатор и восстановление могут быть недостоверны.

### 11. fw4
Ingress (WAN→LAN) и DNAT — зона ответственности fw4, не ksd.

---

## Файлы и пути

| Файл | Назначение |
|------|-----------|
| `/usr/sbin/ksd` | бинарник демона |
| `/etc/init.d/ksd` | procd init-скрипт (START=60) |
| `/etc/init.d/ksd-boot` | установка Layer 0 при загрузке (START=15) |
| `/etc/ksd/emergency.nft` | статический emergency-набор (права 600) |
| `/etc/config/killswitch` | UCI-конфигурация |
| `/var/run/ksd-state.json` | состояние (tmpfs, кэш, восстанавливается) |
| `CONFIG.md` | описание всех опций |
| `BUILD.md` | сборка и развёртывание |
| `docs/DESCRIPTION.zh.md` | описание на китайском |
| `ASSURANCE.md` | assurance contract (`TODO`: статус для v4.0) |

Удалено относительно shell-версии: `/etc/killswitch_vps_elements.cache`, `/etc/killswitch/checksum.md5`, `/var/run/killswitch_mode`, `/var/run/killswitch_wan_if`.

### nft-объекты ksd

```
inet killswitch_table
  ├── counters: dns_leak_drops, invalid_state_drops, quic_drops,
  │             allow_mark, allow_vps, allow_established, output_leak_drops,
  │             (+ версии _fwd), forward_leak_drops
  ├── sets:     vps_ipv4, ks_wan, ks_wan_gw   (динамические)
  │             trusted_ifaces, doh_servers, ntp_bootstrap   (статические)
  ├── chain killswitch_output   (filter/output/-10, policy drop)
  └── chain killswitch_forward  (filter/forward/-15, policy drop)
inet killswitch_mangle   → chain KS_POSTROUTING (-150, accept)
inet ks_emergency        → Layer 0, существует на старте
arp  killswitch_arp      → опционально
```

Всё это принадлежит ksd, вручную ничего в эти таблицы не добавляй.

---

## NTP

Обязательно задай `ntp_servers`. В full-режиме исходящий udp/123 от sysntpd не несёт mark и не входит в VPS-set, поэтому без явного разрешения его режет терминальное правило, часы перестают синхронизироваться, а логика debounce зависит от времени.

```bash
uci add_list killswitch.main.ntp_servers='129.6.15.28'
uci add_list killswitch.main.ntp_servers='132.163.97.1'
uci commit killswitch
/etc/init.d/ksd restart
```

---

## Troubleshooting

### Система в baseline вместо full

```bash
logread -e ksd | grep -i "conntrack utility missing\|verification failed"
ksd status
```

Возможные причины:
- отсутствует утилита `conntrack`;
- WAN не готов;
- ошибка верификации после применения.

Если `full_fail` дошёл до `full_fail_max` (по умолчанию 3), сработал предохранитель: повторная попытка идёт раз в 10 циклов верификации (при 60 с это около 10 минут).

### Verification failed

```bash
logread -e ksd | grep -i "verification failed"
ksd dry-run full
nft list chain inet killswitch_table killswitch_output
```

Проверяются: hook/priority/policy цепей, наличие каждого `comment` из модели, **относительный порядок правил**, наличие счётчиков, содержимое динамических set'ов. Сравни живой набор с выводом `dry-run`.

### VPS set пустой / `Source: DOWN`

```bash
nft list sets table inet passwall2
nft list set inet passwall2 psw2_vps
```

- Имя set'а должно совпадать с `source_set` (по умолчанию `inet passwall2 psw2_vps`), тип — `ipv4_addr`, иначе ksd откажется его использовать.
- Проверь `static_vps` и `merge_static_vps`.
- При `strict_mode=1` пустой set блокирует весь немаркированный трафик. Это ожидаемо.
- Перед очисткой set'а ksd ждёт `empty_source_grace_sec` (15 с).

### `output_leak_drops` медленно растёт

Чаще всего это NTP: задай `ntp_servers`, затем `ksd reset-counters` и наблюдай.

### Счётчики forward всегда нулевые

Вероятно, fw4 flow offloading обходит `killswitch_forward`. Проверь сообщения CRITICAL в `logread -e ksd` и в `ksd self-test`.

### Потерял SSH из LAN

`br-lan` должен быть в `allowed_iface`, иначе output-цепочка блокирует ответы. Восстановление через серийный порт или запасной канал:

```bash
ksd baseline          # понизить до baseline
```

или полный снос:

```bash
service ksd stop
nft destroy table inet killswitch_table
nft destroy table inet killswitch_mangle
nft destroy table inet ks_emergency
nft destroy table arp killswitch_arp
```

Эквивалент через конфиг: `uci set killswitch.main.force_stop=1`, затем `service ksd stop` (демон сам удалит все nft-объекты).

---

## Философия

ksd v4.0 — не абсолютная безопасность, а **локальный гомеостатический механизм**, который:
- ограничивает пространство допустимых траекторий в рамках выбранного trust domain;
- использует одни каузальные законы (nftables), чтобы затруднить другие (утечки);
- держит правила, проверку и документацию в одной модели, чтобы они не расходились;
- честно документирует свои границы.

---

## Когда обращаться к полной документации

- `CONFIG.md`: все опции, значения по умолчанию, ARP-секция, DoH-секция.
- `BUILD.md`: сборка, архитектуры, развёртывание, аварийное снятие.
- `ASSURANCE.md`: claims, evidence, assumptions, residual risks (`TODO`: актуально ли для Go).

---

## Черновик: открытые пункты (удалить перед коммитом)

1. Реальный вывод `ksd status` и `ksd self-test`; расписать поля статуса.
2. Как перечитывается конфиг: `reload` или только `restart`.
3. Что делает `ksd install`, и нужен ли блок с ней.
4. ASSURANCE.md для v4.0: привязка к sha256 бинарника, версия Go, версия документа.
5. Проверить приоритеты (-10/-15/-150) относительно production shell v3.5.
6. Подтвердить ICMP/PMTU-правило и проверку `flow_offloading_hw` в Go.
7. Выровнять README и BUILD.md по схеме init-скриптов (`ksd-boot` + `ksd`).
8. Подтвердить, что lock-файла больше нет.
9. Код на момент составления не был скомпилирован/проверен на роутере (BUILD.md, п. 8): снять или обновить эту пометку после `go vet` и первого запуска.

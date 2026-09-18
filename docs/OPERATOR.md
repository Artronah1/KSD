# Killswitch v3.5 — Операторское руководство

**Версия:** v3.5-final  
**Дата freeze:** Sun Aug 30 19:14:12 2026  
**SHA-256 артефакта:** `e685ca6b0e0062f36614301acf8a2be814eb016424d983b59416b823416039e9`

---

## Что это такое

Killswitch — это fail-close firewall для OpenWrt, который гарантирует, что весь исходящий трафик роутера либо:
- Помечен как VPN (Passwall2 marks)
- Идёт к разрешённым VPS-адресам
- Является частью установленного соединения (established/related)
- Или блокируется

**Ключевые свойства:**
- Атомарное применение правил (nft transaction)
- Автоматический rollback в baseline при ошибках
- Семантическая верификация правил в ядре
- Защита от DNS/DoT/DoQ/DoH утечек
- Периодический мониторинг и самовосстановление

---

## Быстрая диагностика

### Текущее состояние

```bash
/etc/init.d/killswitch status
```

Что смотреть:
- **Mode (intent):** `full` = полная защита, `baseline` = минимальная
- **Conntrack flush:** `ok` = conntrack очищен при изменениях
- **Semantic verify:** `enabled` = включена строгая проверка правил
- **Counters:** сколько пакетов заблокировано (cumulative)

### Быстрая проверка

```bash
/etc/init.d/killswitch test
```

Возвращает `PASS` если все структурные проверки пройдены.

### Генерация правил без применения

```bash
/etc/init.d/killswitch dry_run full      # показать full ruleset
/etc/init.d/killswitch dry_run baseline  # показать baseline ruleset
```

### Логи

```bash
logread -e killswitch | tail -n 50
```

---

## Что делать при изменениях

### 1. Изменение конфигурации (UCI)

```bash
uci set killswitch.main.<параметр>=<значение>
uci commit killswitch
/etc/init.d/killswitch reload
```

Система автоматически обнаружит изменение UCI-hash и пересоберёт ruleset.

### 2. Изменение кода скрипта

**ВАЖНО:** Любое изменение `/etc/init.d/killswitch` нарушает криптографическую привязку к Assurance Contract.

**Процесс:**

1. **Сделай изменения** в коде
2. **Протестируй локально:**
   ```bash
   sh -n /etc/init.d/killswitch              # синтаксис
   /etc/init.d/killswitch dry_run full       # генерация
   /etc/init.d/killswitch test               # самопроверка
   /etc/init.d/killswitch reload             # применение
   /etc/init.d/killswitch status             # проверка состояния
   ```
3. **Вычисли новый хеш:**
   ```bash
   sha256sum /etc/init.d/killswitch
   ```
4. **Обнови Assurance Contract** (`ASSURANCE.md`):
   - Измени версию (например, `v3.5.1` или `v3.6`)
   - Обнови раздел "Artifact Identity" с новым хешем
   - Если изменения влияют на claims/assumptions — обнови соответствующие разделы
5. **Закоммить изменения** в репозиторий вместе с обновлённым `ASSURANCE.md`

### 3. Включение/выключение семантической верификации

```bash
# Включить (строгая проверка правил)
uci set killswitch.main.semantic_verify_enabled=1
uci commit killswitch
/etc/init.d/killswitch reload

# Выключить (только структурная проверка)
uci set killswitch.main.semantic_verify_enabled=0
uci commit killswitch
/etc/init.d/killswitch reload
```

**Рекомендация:** Держи `enabled` если нет проблем с производительностью.

---

## Известные ограничения (что НЕ защищено)

Killswitch защищает **только в рамках заявленной модели угроз**. Следующее **НЕ входит** в защиту:

### 1. Привилегированные локальные атаки
- Root или `CAP_NET_ADMIN` может подделать mark и обойти firewall
- Это осознанное ограничение, не баг

### 2. Established/related трафик
- Установленные соединения доверяются по дизайну
- Если соединение было разрешено, его пакеты проходят

### 3. Неизвестные DoH-серверы
- Блокируются только DoH из списка `doh_servers`
- Произвольные DoH на нестандартных IP не детектируются

### 4. DNS/DoQ на нестандартных портах
- Блокируются только порты 53, 853, 8853 (DNS/DoT), 784 (DoQ)
- DNS на порту 5353 или другом не блокируется явно (но может быть отброшен fail-close если не помечен)

### 5. ICMP covert channels
- ICMP error messages разрешены для PMTU discovery
- Теоретически могут использоваться для скрытых каналов

### 6. Hardware offload
- Если NIC использует hardware offload/fast-path, он может обходить software netfilter
- Текущая верификация покрывает только software path

### 7. Компрометация ядра
- Если ядро скомпрометировано, counters/verifier/recovery могут быть недостоверны

### 8. fw4 ответственность
- Ingress (WAN→LAN) фильтрация и DNAT — зона ответственности fw4, не killswitch

---

## Файлы и пути

| Файл | Назначение |
|------|-----------|
| `/etc/init.d/killswitch` | Основной скрипт (frozen artifact) |
| `/etc/config/killswitch` | UCI конфигурация |
| `/var/run/killswitch_mode` | Текущий режим (`full` или `baseline`) |
| `/var/run/killswitch_wan_if` | Текущий WAN интерфейс |
| `/var/run/killswitch.lock` | Lock файл для атомарности |
| `/etc/killswitch_vps_elements.cache` | Кэш VPS IP-адресов |
| `/etc/killswitch/checksum.md5` | Checksum для обнаружения drift |
| `ASSURANCE.md` | Assurance contract (нормативный документ) |

---

## Контрактные документы

- **ASSURANCE.md** — полная спецификация claims, evidence, assumptions, residual risks
- **Это руководство** — краткая операторская шпаргалка

---

## Когда обращаться к полной документации

Открывай `ASSURANCE.md` если:
- Нужно понять, **почему** система ведёт себя определённым образом
- Нужно обосновать архитектурное решение
- Нужно понять границы защиты для аудита
- Планируешь изменения, влияющие на security model

---

## Troubleshooting

### Система в baseline вместо full

```bash
logread -e killswitch | grep -i "baseline\|conntrack\|error"
```

Возможные причины:
- Отсутствует `conntrack` utility
- WAN интерфейс не готов
- Ошибка верификации после применения

### Semantic verification fails

```bash
logread -e killswitch | grep "DEBUG.*semantics failed"
```

Проверь:
- Правила в ядре соответствуют ожидаемой структуре
- `nft -j list chain inet killswitch_table killswitch_output | jq .`

### VPS set пустой

```bash
/etc/init.d/killswitch status | grep "VPS set elements"
```

Если 0 элементов:
- Проверь Passwall2 source set: `nft list set inet passwall2 psw2_vps`
- Проверь `STATIC_VPS` в конфигурации
- В strict mode пустой set = блокировка всего не-помеченного трафика (это нормально)

---

## Философия

Killswitch v3.5 — это **не абсолютная безопасность**, а **локальный гомеостатический механизм**, который:
- Ограничивает пространство допустимых траекторий в рамках выбранного trust domain
- Использует одни каузальные законы (nftables), чтобы затруднить другие (утечки)
- Честно документирует свои границы

Это инженерный артефакт с явно зафиксированной эпистемической позицией: мы знаем, что защищаем, как доказываем, и где заканчивается наша гарантия.

---

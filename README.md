# LabReserve

Сервис бронирования виртуальных устройств лаборатории. Хранит историю использования, запрещает одновременные активные брони одного устройства и получает диагностику от отдельного gRPC-имитатора.

Бронь действует до явного освобождения. Календарного планирования, временных интервалов и автоматического истечения нет. Все устройства и показания синтетические.

## Возможности и архитектура

- Список устройств с актуальной занятостью из PostgreSQL.
- Создание брони с проверкой входных данных и защитой от конкурентных запросов.
- Освобождение по UUID брони: повтор сохраняет исходное время освобождения и не затрагивает новую бронь.
- История с пагинацией и устойчивым порядком `created_at DESC, id DESC`.
- Диагностика `OK` / `FAULT`, ограничение ожидания и различимые ошибки зависимостей.
- Миграции, OpenAPI 3.1, структурированные JSON-логи и корректное завершение по SIGINT/SIGTERM.

```text
HTTP-клиент → Controller → PostgreSQL
                   └────→ gRPC → Simulator
```

Стек: Go 1.26+, `net/http`, PostgreSQL 16+, `pgx/v5`, goose, gRPC / Protocol Buffers, `log/slog`. SQL написан явно, без ORM.

| Каталог | Назначение |
| --- | --- |
| `cmd/controller`, `cmd/simulator`, `cmd/migrate` | Точки входа приложений и мигратора |
| `internal/service`, `internal/domain` | Валидация, модели и правила работы |
| `internal/storage/postgres` | SQL-запросы и обработка ошибок PostgreSQL |
| `internal/httpapi`, `internal/diagnostics` | HTTP-обработчики, gRPC-клиент и имитатор |
| `api`, `gen` | OpenAPI, исходный protobuf-контракт и сгенерированный код |
| `db` | Миграции и исследование запроса истории |

Предметных таблиц две: `devices` и `reservations`. Активность определяется по `released_at IS NULL`; отдельного флага занятости нет. Частичный уникальный индекс гарантирует одну активную бронь на устройство во всех экземплярах приложения. Конфликт определяется по SQLSTATE и имени индекса, остальные ошибки БД не маскируются под занятость.

Освобождение выполняется одним атомарным `UPDATE` по UUID с сохранением первого `released_at`. История не удаляется. Диагностика только читает состояние, не меняет устройства или брони и не удерживает транзакцию БД во время RPC. Время в API передаётся в UTC, RFC 3339.

## Запуск через Docker Compose

Нужны Docker Engine с Compose и свободный порт 8080. Для Swagger UI также нужен порт 8081.

```sh
git clone https://github.com/IdaemonI/labreserve.git
cd labreserve
cp .env.example .env
docker compose up --build -d
docker compose ps -a
curl -i http://127.0.0.1:8080/readyz
```

После готовности PostgreSQL мигратор применяет схему и завершается с кодом 0, затем запускается контроллер. Первый запуск включает загрузку образов и сборку Go-приложений.

```sh
docker compose logs migrator controller simulator
docker compose --profile docs up -d swagger
# Swagger UI: http://127.0.0.1:8081
docker compose down
```

HTTP опубликован только на `127.0.0.1`; PostgreSQL и gRPC доступны внутри сети Compose. Именованный volume PostgreSQL сохраняется при `restart` и `down`. Команда `down -v` удаляет данные.

Swagger UI отображает [спецификацию OpenAPI 3.1](api/openapi.yaml). Междоменные запросы из интерфейса не настроены; для вызовов используйте curl. `make up`, `make down` и `make logs` вызывают соответствующие команды Compose.

## Локальный запуск без Docker

Установите Go 1.26+ и PostgreSQL 16+. Создайте отдельную роль и базы; пароль задайте при запросе:

```sh
sudo -u postgres createuser --pwprompt labreserve
sudo -u postgres createdb --owner=labreserve labreserve
sudo -u postgres createdb --owner=labreserve labreserve_test
cp .env.example .env
# Укажите свой пароль в DATABASE_URL и TEST_DATABASE_URL.
set -a
. ./.env
set +a
make build
make migrate
```

В двух терминалах с загруженными переменными окружения запустите `./bin/simulator` и `./bin/controller`. Приложения сами не читают `.env`. Миграции можно применить отдельно:

```sh
DATABASE_URL='postgres://labreserve:labreserve_demo_only@127.0.0.1:5432/labreserve?sslmode=disable' make migrate
# Эквивалент: go run ./cmd/migrate up
```

Четыре SQL-миграции встроены в бинарник: таблицы и частичный уникальный индекс, индекс истории, начальные устройства, ограничения владельца. Goose применяет каждую миграцию транзакционно и хранит версии. Повторный запуск безопасен; заполнение использует `ON CONFLICT DO NOTHING`. CLI поддерживает только миграции вперёд.

### Конфигурация

| Переменная | Значение по умолчанию / назначение |
| --- | --- |
| `DATABASE_URL` | Обязательная строка подключения PostgreSQL |
| `HTTP_ADDR` | `:8080`; в примере `.env` — `127.0.0.1:8080` |
| `GRPC_TARGET` | `127.0.0.1:9090` — адрес имитатора для контроллера |
| `DATABASE_TIMEOUT` | `2s` — ожидание операции БД |
| `DIAGNOSTICS_TIMEOUT` | `1s` — deadline RPC, ограниченный контекстом запроса |
| `HTTP_REQUEST_TIMEOUT` | `5s` — контекст HTTP-запроса и таймаут чтения |
| `SHUTDOWN_TIMEOUT` | `10s` — завершение контроллера |
| `GRPC_ADDR` | `:9090` — адрес прослушивания имитатора |
| `SIMULATOR_DELAY` | `3s` для `meter-c1`; допустимо `0s` |
| `SIMULATOR_MODE` | `normal`, `error`, `unavailable` |
| `TEST_DATABASE_URL` | Отдельная БД `labreserve_test` или `labreserve_test_*` |
| `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` | Настройки БД в Compose |
| `HTTP_PORT`, `SWAGGER_PORT` | Порты Compose: `8080`, `8081` |

Таймауты контроллера должны быть положительными и не превышать минуту. Compose формирует внутренний DSN из `POSTGRES_*`; локальные бинарники используют `DATABASE_URL`. Специальные символы в пароле DSN нужно кодировать для URL. Значения `.env.example` предназначены только для локального запуска.

## HTTP API

| Метод и путь | Результат |
| --- | --- |
| `GET /devices` | `200`: устройства и активные брони |
| `POST /reservations` | `201`: новая бронь; `409`: устройство занято |
| `DELETE /reservations/{id}` | `204`: бронь освобождена, включая повтор |
| `GET /devices/{id}/reservations` | `200`: история с `items`, `limit`, `offset` |
| `GET /devices/{id}/diagnostics` | `200`: снимок; `502` / `503` / `504`: ошибка получения |
| `GET /healthz` | `200`: процесс работает |
| `GET /readyz` | `200`: PostgreSQL доступен; иначе `503` |

Неверные параметры дают `400`, неизвестные устройства и брони — `404`, неожиданные внутренние ошибки — `500`. Превышение ожидания зависимости даёт `504`. Все ответы содержат `X-Request-ID`; ошибки имеют общий формат:

```json
{"error":{"code":"device_occupied","message":"Device already has an active reservation"}}
```

Для POST необходим `Content-Type: application/json`, иначе `415`. Максимальный размер тела — 4096 байт, превышение даёт `413`. Неизвестные поля и дополнительные JSON-значения отклоняются. Идентификатор устройства соответствует `[a-z0-9][a-z0-9-]{0,63}`. Владелец после удаления пробелов по краям должен содержать 1–100 Unicode-символов без управляющих символов. UUID освобождения записывается с дефисами.

История по умолчанию использует `limit=20&offset=0`; допустимы `limit` 1–100 и `offset` 0–2147483647. Повторяющиеся параметры пагинации отклоняются. `released_at: null` означает активную бронь.

```sh
curl -sS http://127.0.0.1:8080/devices
curl -i -X POST http://127.0.0.1:8080/reservations \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"sensor-a1","owner":"engineer-01"}'
RESERVATION_ID='uuid-из-ответа'
curl -i -X DELETE "http://127.0.0.1:8080/reservations/$RESERVATION_ID"
curl -i -X DELETE "http://127.0.0.1:8080/reservations/$RESERVATION_ID"
curl -sS 'http://127.0.0.1:8080/devices/sensor-a1/reservations?limit=20&offset=0'
curl -i http://127.0.0.1:8080/devices/sensor-a1/diagnostics
curl -i http://127.0.0.1:8080/devices/adapter-b1/diagnostics
curl -i http://127.0.0.1:8080/devices/meter-c1/diagnostics
```

## gRPC и сценарии диагностики

`InspectDevice` получает ID устройства и возвращает состояние, диагностический код и время наблюдения. Контроллер переиспользует соединение, передаёт контекст, deadline и request ID; имитатор учитывает отмену.

| Устройство | Поведение при стандартных настройках |
| --- | --- |
| `sensor-a1` | `200`, `health: OK` |
| `adapter-b1` | `200`, `health: FAULT`, `INTERFACE_ERROR` |
| `meter-c1` | Задержка 3 секунды при deadline 1 секунда: `504` |

`FAULT` — успешное наблюдение неисправности, а не транспортная ошибка. Недоступность диагностики не блокирует бронирование и не меняет readiness.

```sh
docker compose stop simulator
curl -i http://127.0.0.1:8080/devices/sensor-a1/diagnostics  # 503
curl -i http://127.0.0.1:8080/readyz                         # 200
docker compose start simulator
SIMULATOR_MODE=error docker compose up -d --force-recreate simulator
curl -i http://127.0.0.1:8080/devices/sensor-a1/diagnostics  # 502
SIMULATOR_MODE=normal docker compose up -d --force-recreate simulator
```

После пересоздания дождитесь готовности имитатора. Режим `unavailable` возвращает `503`, а `SIMULATOR_DELAY=0s` убирает задержку измерительного модуля.

[Protobuf-контракт](api/proto/diagnostics.proto) и сгенерированные Go-файлы хранятся в репозитории. Для обычной сборки `protoc` не нужен. Для `make proto` установите официальный `protoc 34.1`; скрипт самостоятельно использует закреплённые `protoc-gen-go v1.36.11` и `protoc-gen-go-grpc v1.6.1`.

## Проверки

```sh
make fmt-check
make vet
make build
make test
make race
make openapi
```

`make fmt` форматирует код. OpenAPI проверяется закреплённым валидатором в отдельном окружении Python; нужны Python 3.10+, модуль `venv` и доступ к пакетам при первом запуске.

Интеграционные тесты требуют настоящего PostgreSQL и отдельной явно указанной тестовой БД:

```sh
export TEST_DATABASE_URL='postgres://labreserve:labreserve_demo_only@127.0.0.1:5432/labreserve_test?sslmode=disable'
make integration
make integration-race
```

Тесты принимают только имена `labreserve_test` / `labreserve_test_*`. Каждый запуск создаёт случайную схему, применяет в ней миграции и удаляет только её; роль должна иметь право создавать схемы. Обычный `go test ./...` не подключается к БД.

Проверяются валидация, HTTP-ошибки, пагинация, gRPC deadline и отмена, повторное освобождение, переподключение и миграции. Конкурентный HTTP-тест запускает 24 клиента и проверяет один `201`, 23 ответа `409` и ровно одну активную строку через SQL. Race detector отдельно проверяет доступ к памяти Go.

```sh
# Нужны стандартные настройки имитатора и свободный sensor-a1.
python3 scripts/demo.py
# Другой адрес: BASE_URL=http://127.0.0.1:18080 python3 scripts/demo.py
```

Сценарий проверяет конкурентные запросы, повторное освобождение и диагностику; освобождает только созданные им брони.

## Исследование SQL

[SQL-команды и результаты](db/analysis/README.md) сравнивают планы запроса истории на 200 000 синтетических завершённых броней до и после индекса `(device_id, created_at DESC, id DESC)`.

```sh
sudo -u postgres createdb --owner=labreserve labreserve_analysis
export ANALYSIS_DATABASE_URL='postgres://labreserve:labreserve_demo_only@127.0.0.1:5432/labreserve_analysis?sslmode=disable'
psql "$ANALYSIS_DATABASE_URL" -X -f db/analysis/history.sql
```

Укажите владельца и реквизиты своей отдельной БД. Скрипт допускает только `labreserve_analysis` / `labreserve_analysis_*`, работает в новой собственной схеме и не удаляет индексы приложения. Для первой страницы сохранён план Index Scan без сортировки; большой OFFSET требует дополнительной работы. Измерения одного локального запуска не являются универсальным бенчмарком.

## Linux и CI

[Unit systemd](deploy/systemd/labreserve.service) запускает контроллер от непривилегированного пользователя. PostgreSQL, миграции и имитатор должны быть подготовлены отдельно.

```sh
make build
sudo useradd --system --home-dir /opt/labreserve --shell /usr/sbin/nologin labreserve
sudo install -d /opt/labreserve/bin /etc/labreserve
sudo install -m 0755 bin/controller /opt/labreserve/bin/controller
sudo install -m 0644 README.md /opt/labreserve/README.md
sudo install -m 0600 /dev/null /etc/labreserve/controller.env
sudoedit /etc/labreserve/controller.env
# DATABASE_URL, HTTP_ADDR=127.0.0.1:8080, GRPC_TARGET; формат KEY=value без export.
sudo install -m 0644 deploy/systemd/labreserve.service /etc/systemd/system/labreserve.service
sudo systemctl daemon-reload
sudo systemctl enable --now labreserve
sudo journalctl -u labreserve -f
ss -ltnp 'sport = :8080'
sudo systemctl stop labreserve
```

При ошибке подключения к БД проверьте журнал, адрес, порт, роль и базу через `pg_isready` / `psql`. При диагностическом `503` проверьте логи имитатора, `GRPC_TARGET` и `./bin/simulator -healthcheck -address 127.0.0.1:9090`; `504` означает превышение ожидания.

[GitHub Actions](.github/workflows/ci.yml) настроен на push и pull request: форматирование, vet, сборка, unit/race и PostgreSQL-тесты, повторное применение миграций, OpenAPI, сборка образа и сценарий Compose. Результат конкретного запуска доступен во вкладке Actions репозитория.

## Ограничения

Аутентификация, авторизация, публичный TLS, ограничение частоты запросов и полноценный мониторинг не реализованы. Сервис предназначен для локального использования; открывать его в публичную сеть без дополнительных мер защиты не следует. Создание брони не идемпотентно: повтор после потерянного ответа может вернуть `409`. К реальному оборудованию сервис не подключается.

# Garmin Personal MCP — русская инструкция

Локальный MCP на Go для официального Garmin Connect Personal API. Это независимый проект, не продукт Garmin. Он читает данные собственного аккаунта через stdio; HTTP-сервера, пользовательской базы и кеша истории здоровья нет.

[Основной README](../README.md) · [Сборки](https://github.com/danchupin/garmin-personal-mcp/releases/latest) · [Безопасность](../SECURITY.md)

## Установка и подключение Codex

Скачайте сборку macOS или Linux для своей архитектуры из Releases и сравните SHA-256 с `SHA256SUMS`. Либо установите через Go:

```sh
GOBIN="$HOME/.local/bin" go install github.com/danchupin/garmin-personal-mcp/cmd/garmin-mcp@v0.3.0
"$HOME/.local/bin/garmin-mcp" login
codex mcp add garmin -- "$HOME/.local/bin/garmin-mcp" stdio
```

`login` читает ключ скрытым вводом, проверяет его через Garmin и сохраняет в приватном файле. После изменения конфигурации перезапустите `garmin` в настройках MCP клиента. По умолчанию доступны wellness и чтение активностей.

Для всех реализованных групп:

```sh
codex mcp add garmin --env GARMIN_SCOPES='wellness:read activities:read activities:export body:read nutrition:read womens_health:read profile:read fitness_age:read' -- "$HOME/.local/bin/garmin-mcp" stdio
```

Создайте собственный ключ по [инструкции Garmin](https://developer.garmin.com/gc-developer-program/personal/personal-faq/). Не передавайте ключ в чат, URL, аргументы команды или issue. Лицензия MIT относится к нашему коду, а не к сервисам Garmin.

## Где хранится ключ

На macOS: `~/Library/Application Support/GarminMCP/personal-api-key`. На Linux: `$XDG_CONFIG_HOME/GarminMCP/personal-api-key` либо `~/.config/GarminMCP/personal-api-key`. Права каталога — `0700`, файла — `0600`; файл доступен другим программам того же пользователя ОС. Это не зашифрованный vault.

Свой абсолютный путь задаётся через `GARMIN_PERSONAL_API_KEY_FILE` одинаково для `login` и MCP-клиента. Регион — `GARMIN_REGION=global` или `china`.

Временный Garmin token хранится в RAM и автоматически обновляется следующим запросом за две минуты до истечения. Отдельного refresh token нет. После неудачного обмена действует минутная пауза. Для замены ключа повторите `login` и перезапустите MCP; при утечке отзовите старый ключ в Garmin Connect.

## Что можно запросить

- Сон, HRV, пульс, стресс, Body Battery, шаги, SpO₂ и гидратацию.
- Вес, питание, fitness age, menstrual health и настройки профиля — если данные доступны аккаунту.
- Активности, их детали, полный JSON и оригинальный файл активности.

Каталог содержит три инструмента: `connection_status`, `list_garmin_capabilities`, `read_garmin`. Сначала запросите схему конкретной операции. Для экономии контекста используйте `fields`, `max_items`, `offset`; проверяйте `projection`, где указаны усечения. Отсутствующие значения не являются нулями.

Пример: «Покажи сон, HRV, пульс покоя и стресс за последние 14 завершённых дней; укажи покрытие каждого показателя». Для сегодняшних данных учитывайте время последней синхронизации и неполный день.

Экспорт возвращает MCP resource link. Чтение снова обращается к Garmin; файл на сервере не хранится. Ссылка действует до десяти минут и не переживает перезапуск процесса. Клиент должен поддерживать MCP resources; большие файлы требуют сообщений до 36 MiB.

## Разработка

```sh
git clone https://github.com/danchupin/garmin-personal-mcp.git
cd garmin-personal-mcp
make check
make build
make release
```

Go toolchain — 1.27.3. Python 3 нужен для упаковки и её тестов. Тесты работают с синтетическими credentials. Подробности и границы проверки — в [verification.md](verification.md).

Версия предназначена для локального stdio. Подключение облачного/мобильного ChatGPT и загрузка тренировок на часы не реализованы. Независимый security audit не проводился. Клиент может сохранять полученные данные в истории чата, хотя сам MCP их на диск не записывает.

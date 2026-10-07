# Работа в продакшене

Схема: клиент (сайт, чат-бот, helpdesk) → nginx (TLS, доступ) → `localrag serve` →
Ollama (эмбеддинги + чат-модель). Всё на одном сервере, наружу ничего не уходит,
если не включено облако ollama.com.

Примеры ниже (systemd, nginx) — шаблоны; в вашем окружении они не проверялись.

## 1. Сборка

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o localrag ./cmd/localrag   # или arm64
```

Один статический бинарник, зависимостей нет. Для PDF на сервере нужен `pdftotext`
(`apt install poppler-utils`).

## 2. Раскладка на сервере

```
/opt/localrag/localrag        бинарник
/etc/localrag/config.yaml     конфиг
/srv/localrag/docs/           статьи (лучше git-клон отдельного репозитория со статьями)
/var/lib/localrag/localrag.db индекс
```

В config.yaml пути абсолютные:

```yaml
docs_dir: /srv/localrag/docs
db_path: /var/lib/localrag/localrag.db
search:
  min_score: 0.81      # из calibrate на ваших статьях
  max_gap: 0.03
unanswered_log: /var/lib/localrag/unanswered.jsonl
fallback_message: |
  Nie znaleźliśmy odpowiedzi na to pytanie w bazie wiedzy.
  Napisz do nas na <адрес> lub zadzwoń pod numer <телефон>.
language:
  expected: pl
  other_message: |
    No information is available. Please write to <адрес> or call <телефон>.
server:
  addr: 127.0.0.1:8080  # наружу только через nginx
```

## 3. Ollama

```bash
ollama pull embeddinggemma
ollama pull gemma4
```

Полезные переменные окружения сервиса Ollama:

- `OLLAMA_KEEP_ALIVE=24h` — держать модели в памяти, иначе первый запрос после паузы
  ждёт загрузки модели;
- `OLLAMA_NUM_PARALLEL` — сколько запросов модель обрабатывает одновременно. Остальные
  ждут в очереди, поэтому при нескольких пользователях время ответа растёт.

Основное время ответа — генерация LLM (секунды), поиск занимает миллисекунды. Требования
к памяти и GPU определяет выбранная чат-модель; на конкретном железе их нужно замерить.

## 4. systemd

`/etc/systemd/system/localrag.service`:

```ini
[Unit]
Description=localrag knowledge base API
After=network-online.target ollama.service
Wants=ollama.service

[Service]
User=localrag
ExecStart=/opt/localrag/localrag -config /etc/localrag/config.yaml serve
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
# для ollama.com: OLLAMA_API_KEY=... в этом файле, права 600
EnvironmentFile=-/etc/localrag/env

[Install]
WantedBy=multi-user.target
```

```bash
sudo -u localrag /opt/localrag/localrag -config /etc/localrag/config.yaml index
systemctl enable --now localrag
curl -s localhost:8080/healthz      # {"status": "ok", "chunks": 96}
```

- `systemctl reload localrag` — перечитать индекс после `index` (SIGHUP).
- `systemctl restart localrag` — после любой правки config.yaml: конфиг читается только при старте.

## 5. Обновление базы

```bash
cd /srv/localrag/docs && git pull
sudo -u localrag /opt/localrag/localrag -config /etc/localrag/config.yaml index
systemctl reload localrag
```

Можно повесить на cron или на CI после мержа в репозиторий статей. `index` безопасно
запускать при работающем `serve`. Подробности — [knowledge-base.md](knowledge-base.md)
и [index.md](index.md).

## 6. Доступ снаружи

У `serve` нет авторизации, TLS и ограничения частоты запросов. Встроено только ограничение
тела запроса 64 КБ. Поэтому слушать он должен только 127.0.0.1, а наружу — через nginx:

```nginx
server {
    listen 443 ssl;
    server_name kb.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    location /ask {
        auth_basic "localrag";                    # или allow/deny по IP, или токен
        auth_basic_user_file /etc/nginx/localrag.htpasswd;
        limit_req zone=localrag burst=5;
        proxy_pass http://127.0.0.1:8080;
        proxy_read_timeout 310s;                  # больше answer.timeout_sec (300)
    }
}
# в http {}: limit_req_zone $binary_remote_addr zone=localrag:10m rate=30r/m;
```

`/healthz` наружу открывать не нужно, он для мониторинга.

## 7. API для клиента

```bash
curl -s https://kb.example.com/ask -u user:pass -d '{"question": "Outlook pisze, że pracuję offline"}'
```

```json
{
  "answered": true,
  "answer": "**Kroki:** ...",
  "top_score": 0.94,
  "sources": [{"path": "outlook-work-offline.md", "title": "Outlook: praca w trybie offline (Working Offline)",
               "url": "https://support.microsoft.com/...", "score": 0.94, "text": "..."}]
}
```

- Клиент всегда показывает `answer`: при `answered: false` там уже лежит заготовка
  (польская или английская).
- `reason` при `answered: false`: `language`, `below_threshold` или `llm_no_answer`.
- `sources` — для ссылок «подробнее» (`title` + `url`); при заготовке пусто.
- Коды ответа: `200` — ответ или заготовка; `400` — пустой вопрос или не JSON;
  `502` — недоступна Ollama или модель (текст ошибки в `error`). На 502 клиенту стоит
  показать ту же контактную заготовку.

## 8. Мониторинг

- `GET /healthz` → `chunks` больше 0. Он проверяет только сам процесс и индекс,
  но не Ollama. Доступность моделей проверяйте отдельно (`curl localhost:11434/api/tags`)
  или по ответам 502.
- Логи: `journalctl -u localrag`. `serve` пишет только старт, перезагрузку индекса и ошибки.
- Вопросы без ответа: `unanswered_log: /var/lib/localrag/unanswered.jsonl`. Каждая заготовка
  дописывает строку с вопросом, причиной (`language`, `below_threshold`, `llm_no_answer`),
  лучшим score и ближайшей статьёй. Файл открывается на каждую запись, поэтому его можно
  ротировать обычным logrotate. Права 600: в нём тексты вопросов пользователей, то есть,
  возможно, персональные данные. Как с ним работать — [knowledge-base.md](knowledge-base.md).

## 9. Приватность и ollama.com

С локальной Ollama данные не покидают сервер. Если в `answer` или `embedding` указан
`https://ollama.com/v1`, в облако уходят вопросы пользователей и тексты найденных статей
(при облачных эмбеддингах — ещё и все статьи при индексации). Ключ задаётся через
`api_key: ${OLLAMA_API_KEY}` и `EnvironmentFile`, не храните его в config.yaml.

## 10. Чек-лист перед запуском

- [ ] Статьи в `docs_dir`, `index` без строк `skipped`.
- [ ] `calibrate` на актуальных статьях, `min_score` выставлен по нему.
- [ ] Опасные вопросы (похожие, но без ответа) через `ask` дают заготовку.
- [ ] `fallback_message` и `language.other_message` с настоящими контактами.
- [ ] `server.addr` = `127.0.0.1:...`, снаружи nginx с TLS и доступом.
- [ ] `OLLAMA_KEEP_ALIVE`, модели загружены, первый ответ приходит без долгой загрузки.
- [ ] Обновление статей: `index` + `systemctl reload localrag` описано в регламенте или автоматизировано.
- [ ] `unanswered_log` включён, кто-то регулярно его разбирает.

## Известные ограничения

- Проверка польского языка эвристическая. Короткий английский вопрос без служебных слов
  проходит её и отсекается уже LLM. В режиме `answer.mode: fragments` (без LLM) этой
  второй проверки нет.
- Если такой английский вопрос не наберёт `min_score`, пользователь получит польскую
  заготовку, а не английскую.
- Один процесс, индекс в памяти. На тысячах статей это не проблема; отдельная векторная
  БД (Qdrant) понадобится на сотнях тысяч векторов или при общем индексе для нескольких сервисов.

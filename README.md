# local-emb-rag

Локальный поиск по базе знаний. Отвечает только по вашим документам, а если
ответа в них нет, выдаёт заранее заданную заготовку («данных нет, пишите/звоните …»).
С локальной Ollama в интернет ничего не уходит (облако ollama.com — по желанию, см. ниже).

Вопросы ожидаются на польском. На вопрос на другом языке сразу, без поиска,
возвращается английская заготовка `language.other_message`.

```
вопрос → эмбеддинг (EmbeddingGemma 2) → ближайшие фрагменты из индекса
       → сходство ≥ min_score ? ответ локальной LLM по фрагментам + источники
                              : fallback_message из конфига
```

Если LLM видит, что во фрагментах ответа всё-таки нет, она отвечает `NO_ANSWER`,
и пользователь тоже получает заготовку.

## Что нужно

- Go 1.24+ (сборка без CGO, один бинарник).
- Сервер моделей с OpenAI-совместимым API: [Ollama](https://ollama.com) или `llama-server` из llama.cpp.
- Для PDF: `pdftotext` (пакет `poppler-utils`, на macOS `brew install poppler`).

## Формат документов

Поддерживаются `.md .markdown .txt .rst .pdf` (только UTF-8; PDF с текстовым слоем).
Лучше всего работают md-статьи с YAML front matter:

```markdown
---
id: chrome-crashes
title: Chrome się zawiesza lub zamyka
questions:
  - Chrome ciągle się zamyka, co zrobić?
  - Why does Chrome keep crashing?
keywords: [chrome, awaria, crash]
sourceName: Pomoc Google Chrome
sourceUrl: https://support.google.com/chrome/answer/95669
---
## Objawy
...
```

- `title` идёт в префикс эмбеддинга и в список источников.
- Каждый вопрос из `questions` и строка `keywords` индексируются отдельными векторами
  и ведут на свою статью: вопрос пользователя, похожий на заготовленный, находит статью точнее.
- `sourceUrl` показывается рядом с источником; `id`, `checkedAt` и прочие служебные поля
  в поиск не попадают.
- Файлы без front matter индексируются целиком, заголовок берётся из имени файла.

Примеры: `examples/docs/` (польские тестовые статьи) и `examples/questions.jsonl`.

## Быстрый старт

```bash
go build -o localrag ./cmd/localrag
cp config.example.yaml config.yaml          # docs_dir, модели, текст заготовки

# модели (имена зависят от того, как вы их загрузили)
ollama pull gemma4                          # чат-модель для ответов
ollama pull embeddinggemma                  # EmbeddingGemma v1, 768 измерений
# EmbeddingGemma 2: имя в Ollama проверьте сами; либо GGUF через llama-server:
#   llama-server -m embeddinggemma-2-*.gguf --embedding --port 8081
#   и embedding.base_url: http://localhost:8081/v1

./localrag index                            # индексирует docs_dir, повторный запуск берёт только изменённые файлы
./localrag ask "Chrome ciągle się zamyka, co zrobić?"
./localrag serve                            # HTTP API
```

HTTP API:

```bash
curl -s localhost:8080/ask -d '{"question":"Chrome ciągle się zamyka, co zrobić?"}'
# {"answered": true, "answer": "...", "top_score": 0.81,
#  "sources": [{"path": "chrome-crashes.md", "title": "...", "url": "https://...", "score": 0.81, "text": "..."}]}
# answered=false — вернулась заготовка, reason: language | below_threshold | llm_no_answer
curl -s localhost:8080/healthz
kill -HUP <pid>                             # перечитать индекс после `localrag index`
```

## Порог «ответа нет»: `calibrate`

Главная настройка — `search.min_score`. Подберите её на своих вопросах:

```jsonl
{"q": "Chrome ciągle się zamyka, co zrobić?", "relevant": ["chrome-crashes.md"]}
{"q": "Ile dni urlopu mi przysługuje?", "relevant": []}
```

`relevant: []` — вопрос, на который в базе ответа нет (нужна заготовка).
Возьмите 20–30 обычных вопросов и 10–15 «чужих».

```bash
./localrag calibrate questions.jsonl
```

Команда покажет лучший score по каждому вопросу и предложит `min_score`,
при котором больше всего вопросов попадает на нужную сторону. Пример данных: `examples/`.

## Облако ollama.com

Любую часть можно перенести в облако Ollama: в `base_url` пишется `https://ollama.com/v1`,
в `api_key` — ключ (создаётся на https://ollama.com/settings/keys). Ключ лучше держать
в переменной окружения: в конфиге `${VAR}` в `api_key` и `base_url` подставляется при запуске.

```yaml
answer:
  base_url: https://ollama.com/v1
  model: gemma4:31b            # облачная модель из каталога ollama.com
  api_key: ${OLLAMA_API_KEY}
```

```bash
export OLLAMA_API_KEY=...
./localrag ask "..."
```

Если `base_url` указывает на ollama.com, а ключа нет, localrag не запустится и скажет, какой
ключ не задан. Ключ отправляется заголовком `Authorization: Bearer ...`; для локальной Ollama он не нужен.

Что важно знать:
- в облако уходят вопрос и тексты найденных статей (а при облачных эмбеддингах ещё и все статьи
  при индексации). Эмбеддинги разумно оставить локальными;
- есть ли в облаке ollama.com модели эмбеддингов (и EmbeddingGemma в частности), я не проверял.
  Если перенесёте эмбеддинги, после смены модели выполните `localrag index`.

## Настройки, которые стоит знать

- `embedding.dims: 256` — вектор EmbeddingGemma 2 обрезается до 256 (Matryoshka).
  На нашем тесте качество не упало, индекс в 3 раза меньше.
- Смена модели, `dims`, `doc_prefix`, `query_prefix` или `chunk_chars` автоматически перестраивает индекс целиком.
  Обновление localrag со сменой формата индекса тоже: первый `localrag index` после обновления индексирует всё заново.
- `language.expected: pl` — проверка языка вопроса (польские буквы и служебные слова). Сомнительный
  короткий вопрос без диакритики считается польским и идёт в поиск. `""` — выключить проверку.
- `search.max_gap: 0.05` — в ответ и источники попадают только фрагменты, близкие к лучшему.
- `embedding.batch_size: 1` — Ollama на EmbeddingGemma портила векторы при пакетной отправке.
  Если поставить больше, `index` сначала сравнит пакетный и поштучный результат и при расхождении
  сам перейдёт на поштучный режим с предупреждением.
- `answer.mode: fragments` — без LLM, отдаются найденные фрагменты.
- Модель эмбеддингов меняется в конфиге; для BGE-M3 пример есть в `config.example.yaml`.
- EmbeddingGemma 2 нельзя запускать в FP16: выдаёт NaN. Если такое случится, `index` упадёт с понятной ошибкой.

## Устройство

| пакет | что делает |
|---|---|
| `internal/ingest` | чтение md/txt/rst/pdf, разбор front matter, нарезка на фрагменты |
| `internal/lang` | проверка, что вопрос на польском |
| `internal/embed` | клиент `/v1/embeddings`, обрезка и нормализация векторов |
| `internal/store` | SQLite (pure Go), поиск перебором по косинусу в памяти |
| `internal/rag` | индексация, поиск с порогом, ответ или заготовка, калибровка |
| `internal/llm` | клиент `/v1/chat/completions` |
| `internal/server` | HTTP API |

Поиск перебором в памяти: на десятках тысяч фрагментов это миллисекунды,
отдельная векторная БД на таком объёме не нужна.

```bash
go test ./...
```

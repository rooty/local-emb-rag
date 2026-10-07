# local-emb-rag

Локальный поиск по базе знаний. Отвечает только по вашим документам, а если
ответа в них нет, выдаёт заранее заданную заготовку («данных нет, пишите/звоните …»).
В интернет ничего не уходит.

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

## Быстрый старт

```bash
go build -o localrag ./cmd/localrag
cp config.example.yaml config.yaml          # docs_dir, модели, текст заготовки

# модели (имена зависят от того, как вы их загрузили)
ollama pull gemma4                          # чат-модель для ответов
# EmbeddingGemma 2: имя в Ollama проверьте сами; либо GGUF через llama-server:
#   llama-server -m embeddinggemma-2-*.gguf --embedding --port 8081
#   и embedding.base_url: http://localhost:8081/v1

./localrag index                            # индексирует docs_dir, повторный запуск берёт только изменённые файлы
./localrag ask "как продлить сертификат"
./localrag serve                            # HTTP API
```

HTTP API:

```bash
curl -s localhost:8080/ask -d '{"question":"как продлить сертификат"}'
# {"answered": true, "answer": "...", "top_score": 0.71, "sources": [{"path": "tls.md", "score": 0.71, "text": "..."}]}
# answered=false — вернулась заготовка, reason: below_threshold | llm_no_answer
curl -s localhost:8080/healthz
kill -HUP <pid>                             # перечитать индекс после `localrag index`
```

## Порог «ответа нет»: `calibrate`

Главная настройка — `search.min_score`. Подберите её на своих вопросах:

```jsonl
{"q": "как продлить сертификат", "relevant": ["infra/tls.md"]}
{"q": "сколько дней отпуска положено", "relevant": []}
```

`relevant: []` — вопрос, на который в базе ответа нет (нужна заготовка).
Возьмите 20–30 обычных вопросов и 10–15 «чужих».

```bash
./localrag calibrate questions.jsonl
```

Команда покажет лучший score по каждому вопросу и предложит `min_score`,
при котором больше всего вопросов попадает на нужную сторону. Пример данных: `examples/`.

## Настройки, которые стоит знать

- `embedding.dims: 256` — вектор EmbeddingGemma 2 обрезается до 256 (Matryoshka).
  На нашем тесте качество не упало, индекс в 3 раза меньше.
- Смена модели, `dims`, `doc_prefix` или `chunk_chars` автоматически перестраивает индекс целиком.
- `answer.mode: fragments` — без LLM, отдаются найденные фрагменты.
- Модель эмбеддингов меняется в конфиге; для BGE-M3 пример есть в `config.example.yaml`.
- EmbeddingGemma 2 нельзя запускать в FP16: выдаёт NaN. Если такое случится, `index` упадёт с понятной ошибкой.

## Устройство

| пакет | что делает |
|---|---|
| `internal/ingest` | чтение md/txt/rst/pdf, нарезка на фрагменты по абзацам и предложениям |
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

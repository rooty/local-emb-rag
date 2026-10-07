# Пополнение базы знаний

База знаний — это папка `docs_dir` из config.yaml. localrag отвечает только по файлам
в ней; всё, чего там нет, получает заготовку `fallback_message`.

## Формат статьи

Лучше всего работают md-статьи с YAML front matter, по одной проблеме на статью:

```markdown
---
id: "chrome-popup-blocked"
title: "Chrome: zablokowano potrzebne wyskakujące okienko"
category: "browsers"
language: "pl"
questions: ["Chrome zablokował potrzebne wyskakujące okienko", "Chrome nie otwiera okienka logowania na stronie"]
keywords: ["popup blocked", "wyskakujące okienko", "blokada popup"]
sourceName: "Google Chrome Help"
sourceUrl: "https://support.google.com/chrome/answer/95472"
checkedAt: "2026-10-03"
---
## Objawy
...
## Kroki
1. ...
## Weryfikacja
...
## Eskalacja
...
```

Что из этого использует localrag:

| поле | как используется |
|---|---|
| `title` | идёт в эмбеддинг статьи и в список источников ответа. Без него берётся имя файла |
| `questions` | каждый вопрос — отдельный вектор, ведущий на статью. Главный рычаг качества поиска |
| `keywords` | все ключевые слова одной строкой — ещё один вектор |
| `sourceUrl` | показывается рядом с источником в ответе |
| тело статьи | режется на фрагменты по `chunk_chars` (1200 символов), идёт в эмбеддинг и в LLM |
| `id`, `category`, `language`, `sourceName`, `checkedAt` | пока не используются, в поиск не попадают |

Другие форматы: `.markdown .txt .rst .pdf`. Только UTF-8; PDF только с текстовым слоем
и при установленном `pdftotext` (`brew install poppler`). Файлы без front matter
индексируются целиком, заголовок берётся из имени файла. Скрытые папки (`.git` и т.п.)
пропускаются.

## Как писать `questions`

Поиск сравнивает вопрос пользователя с вопросами из статьи, поэтому:

- пишите так, как спрашивают пользователи, а не как называется раздел:
  «Outlook pisze, że pracuję offline», а не «Tryb offline w programie Outlook»;
- 4–6 разных формулировок одной проблемы: жалоба («nie działa…»), вопрос «jak…», симптом;
- можно добавить вариант без диакритики («sie nie laduje») — так часто пишут с телефона;
- английские вопросы в статьях не мешают, но и не помогают: вопрос не на польском
  получает английскую заготовку ещё до поиска;
- если пользователи попадают не в ту статью, добавьте их реальную формулировку
  в `questions` нужной статьи. Это лечит промахи лучше, чем подбор порога.

## Добавить, изменить, удалить статью

```bash
cp new-article.md docs/                    # или правка / удаление файла в docs/
./localrag -config config.yaml index       # пересчитываются только новые и изменённые файлы
kill -HUP $(pgrep -f 'localrag.*serve')    # если запущен serve: перечитать индекс
```

- Изменённым считается файл с другим содержимым (SHA-256), в том числе после правки
  только front matter.
- Удалённые из папки файлы удаляются из индекса при следующем `index`.
- Строки `skipped <файл>: <причина>` в выводе `index` — файлы, которые не попали в базу
  (не UTF-8, ошибка в front matter, PDF без текста). Их нужно исправить.

## Проверка после изменений

```bash
./localrag -config config.yaml ask "Chrome nie otwiera okienka logowania"
./localrag -config config.yaml calibrate examples/questions.jsonl
```

`ask` показывает ответ и источники (title, файл, score, ссылка). Если вместо ответа
пришла заготовка, внизу видна причина:

- `below_threshold` — ничего не набрало `min_score`: в базе нет статьи или ей не хватает
  `questions` с такой формулировкой;
- `llm_no_answer` — статья нашлась, но LLM решила, что ответа в ней нет;
- `language` — вопрос не на польском.

## Чего не хватает в базе: `unanswered_log`

Если в config.yaml задан `unanswered_log`, каждый вопрос, получивший заготовку, дописывается
в этот файл, и через `ask`, и через `serve`:

```json
{"time":"2026-10-07T18:50:00Z","q":"Jak dodać podpis w Outlooku?","reason":"below_threshold","top_score":0.789,"top_path":"outlook-manual-sync.md"}
```

Как разбирать:

- `reason: below_threshold` с низким score — темы, которых нет в базе. Повторяются — пишите статью;
- `below_threshold` со score чуть ниже `min_score` и подходящей `top_path` — статья есть,
  но вопрос сформулирован иначе: добавьте формулировку в её `questions`;
- `llm_no_answer` — статья нашлась, но ответа в ней нет: её стоит дополнить
  или написать отдельную;
- `language` — вопросы не на польском.

Файл подходит для `calibrate` как есть (все вопросы в нём считаются «ответа в базе нет»):

```bash
./localrag -config config.yaml calibrate unanswered.jsonl
```

После того как вы написали статьи, перенесите их вопросы в `examples/questions.jsonl`
с нужным `relevant`, а файл очистите (`: > unanswered.jsonl`).

## Набор проверочных вопросов

`examples/questions.jsonl` — вопросы для `calibrate`, по одному JSON на строку:

```jsonl
{"q": "Outlook pisze, że pracuję offline", "relevant": ["outlook-work-offline.md"]}
{"q": "Ile dni urlopu mi przysługuje?", "relevant": []}
```

- `relevant` — путь статьи относительно `docs_dir`; `[]` — в базе ответа нет.
- Вопросы должны быть перефразами, а не копиями `questions` из статей, иначе оценка завышена.
- Добавляя статьи, добавляйте 1–2 вопроса к ним. Если «чужой» вопрос из набора теперь
  покрыт новой статьёй, перенесите его в «в базе».
- Держите 10–15 вопросов, ответа на которые нет, но они похожи на базу (Firefox при
  статьях о Chrome, Wi-Fi на Windows при статьях о macOS) — именно они проверяют порог.

После заметного пополнения базы прогоните `calibrate` и при необходимости поправьте
`search.min_score` (см. [index.md](index.md)).

# Релизы: выпуск и скачивание

Бинарники собирает GitHub Actions (`.github/workflows/build.yml`):

- каждый push и PR в `main`: тесты + сборка под linux/darwin × amd64/arm64,
  архивы лежат в artifacts запуска (вкладка Actions), в релиз не попадают;
- тег `v*`: то же самое + GitHub Release с архивами и `SHA256SUMS`.

Архивы называются `localrag_<os>_<arch>.tar.gz` (без версии в имени), внутри
`localrag`, `README.md`, `config.example.yaml`.

## Выпустить новый релиз

1. Убедитесь, что последний запуск на `main` зелёный (Actions → build).
2. Выберите номер по semver: `v0.1.1` — исправления, `v0.2.0` — новые возможности,
   `v1.0.0` — несовместимые изменения (например, формата конфига).
3. Поставьте тег на `main` и отправьте его:

   ```bash
   git checkout main && git pull
   git tag -a v0.2.0 -m "v0.2.0"
   git push origin v0.2.0
   ```

4. Через несколько минут релиз появится на странице Releases. Описание собирается
   автоматически из смерженных PR; его можно поправить в GitHub.

Проверка:

```bash
gh release view v0.2.0 --repo rooty/local-emb-rag     # 4 архива + SHA256SUMS
```

Если сборка по тегу упала:

- исправьте причину в `main`;
- выпустите следующий номер (`v0.2.1`). Опубликованные теги лучше не переиспользовать:
  у кого-то уже может быть скачана версия с этим номером;
- если релиз так и не появился и тег никто не скачивал, можно удалить тег и поставить заново:
  `gh release delete v0.2.0 --cleanup-tag --yes` (или `git push --delete origin v0.2.0`).

Версия в сам бинарник сейчас не прошивается (`localrag` не печатает версию);
ориентируйтесь на имя релиза.

## Скачать последний релиз из консоли

Репозиторий приватный, поэтому скачивание требует авторизации GitHub. Анонимный
`curl https://github.com/.../releases/latest/download/...` вернёт 404.

Платформа для имени архива:

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')          # linux | darwin
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; esac
F=localrag_${OS}_${ARCH}.tar.gz
```

### Вариант 1: gh (проще всего)

```bash
gh auth login                                        # один раз
gh release download --repo rooty/local-emb-rag --pattern "$F" --pattern SHA256SUMS --clobber
```

Без `--pattern` gh скачает все файлы последнего релиза; конкретная версия — `gh release download v0.1.0 ...`.

### Вариант 2: curl с токеном

Нужен токен GitHub с правом чтения репозитория: fine-grained token с доступом к
`rooty/local-emb-rag` и разрешением **Contents: Read-only** (Settings → Developer settings →
Personal access tokens). Для jq на macOS: `brew install jq`.

```bash
export GITHUB_TOKEN=github_pat_...
R=https://api.github.com/repos/rooty/local-emb-rag
ID=$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" $R/releases/latest \
     | jq -r --arg f "$F" '.assets[] | select(.name==$f).id')
curl -fSL -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/octet-stream" \
     -o "$F" $R/releases/assets/$ID
```

То же через wget:

```bash
wget -q --header "Authorization: Bearer $GITHUB_TOKEN" --header "Accept: application/octet-stream" \
     -O "$F" $R/releases/assets/$ID
```

Конкретная версия вместо последней: `$R/releases/tags/v0.1.0` вместо `$R/releases/latest`.
`SHA256SUMS` скачивается так же (подставьте `F=SHA256SUMS` в поиск `ID`).

### Если репозиторий станет публичным

Токен не понадобится, работает постоянная ссылка на последний релиз:

```bash
curl -fsSLO https://github.com/rooty/local-emb-rag/releases/latest/download/$F
wget https://github.com/rooty/local-emb-rag/releases/latest/download/$F
```

(Это стандартный адрес GitHub; пока репозиторий приватный, проверить его нельзя.)

## Проверить и установить

```bash
sha256sum -c --ignore-missing SHA256SUMS                     # Linux
grep " $F\$" SHA256SUMS | shasum -a 256 -c -                 # macOS

tar xzf "$F"
sudo install -m 755 "${F%.tar.gz}/localrag" /usr/local/bin/localrag
xattr -d com.apple.quarantine /usr/local/bin/localrag 2>/dev/null   # только macOS: бинарник не подписан
localrag                                                     # печатает список команд
```

Обновление на сервере: скачать новый архив, заменить бинарник, `systemctl restart localrag`.
Если в описании релиза сказано, что изменился формат индекса, перед рестартом выполните `localrag index`
(см. [index.md](index.md)).

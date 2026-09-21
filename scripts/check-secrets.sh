#!/usr/bin/env bash
# Ищет в отслеживаемых файлах то, что не должно попадать в публичный репозиторий:
# токен бота Telegram, токен T-Invest, логин-пароль прокси, строку сессии MTProto.
# Запускается в CI на каждый push и как pre-commit хук (см. CONTRIBUTING.md).
set -u

# Заведомо ненастоящие значения: сам сканер, примеры в .env.example и адреса из
# документационных диапазонов RFC 5737, которыми пользуются тесты.
ALLOW='scripts/check-secrets\.sh|\.github/workflows/secret-scan\.yml|\.env\.example|user:pass@|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.'

fail=0
scan() {
  local name="$1" pattern="$2"
  local hits
  hits=$(git grep -InE "$pattern" -- . ':!*.lock' ':!*package-lock.json' 2>/dev/null | grep -vE "$ALLOW" || true)
  if [ -n "$hits" ]; then
    echo "!! $name — найдено в:"
    echo "$hits" | cut -d: -f1,2 | sed "s/^/   /"
    fail=1
  fi
}

scan "токен Telegram-бота"      '[0-9]{8,10}:AA[A-Za-z0-9_-]{30,}'
scan "токен T-Invest"           't\.[A-Za-z0-9_-]{50,}'
scan "логин-пароль в URL"       '[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+@[0-9]{1,3}(\.[0-9]{1,3}){3}:[0-9]{2,5}'
scan "приватный ключ"           'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY'

if [ "$fail" -ne 0 ]; then
  echo
  echo "Секрет в коде. Вынести в .env, а утёкшее значение считать сгоревшим и перевыпустить."
  exit 1
fi
echo "секретов не найдено"

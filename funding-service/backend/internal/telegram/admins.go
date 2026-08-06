package telegram

import (
	"strconv"
	"strings"
)

// AdminList — разобранный TELEGRAM_ADMINS: набор ключей, каждый из которых
// опознаёт админа либо по числовому chat_id, либо по @username (регистр не важен —
// Telegram сам его не различает). Роль пересчитывается при каждом /start, поэтому
// правка переменной применяется без переезда данных: достаточно перезапуска.
type AdminList map[string]struct{}

// ParseAdmins принимает записи вида "123456789", "@user", "user" —
// в любом порядке и с любыми пробелами. Пустые элементы игнорируются.
func ParseAdmins(entries []string) AdminList {
	list := make(AdminList)
	for _, raw := range entries {
		for _, part := range strings.Split(raw, ",") {
			key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(part), "@"))
			if key != "" {
				list[key] = struct{}{}
			}
		}
	}
	return list
}

// Has сообщает, значится ли этот чат в списке админов — по id или по username.
func (a AdminList) Has(chatID int64, username string) bool {
	if len(a) == 0 {
		return false
	}
	if _, ok := a[strconv.FormatInt(chatID, 10)]; ok {
		return true
	}
	if username == "" {
		return false
	}
	_, ok := a[strings.ToLower(strings.TrimPrefix(username, "@"))]
	return ok
}

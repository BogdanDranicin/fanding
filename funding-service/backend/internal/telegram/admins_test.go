package telegram

import "testing"

func TestParseAdminsAndHas(t *testing.T) {
	// Одна переменная окружения может приехать как одна строка с запятыми
	// (envconfig уже режет её сам, но пробелы и @ остаются на нас).
	admins := ParseAdmins([]string{" 123456789 , @Boss ", "", "second_admin"})

	if len(admins) != 3 {
		t.Fatalf("ожидалось 3 записи, получено %d: %v", len(admins), admins)
	}

	cases := []struct {
		name     string
		chatID   int64
		username string
		want     bool
	}{
		{"по chat_id", 123456789, "", true},
		{"по username с @ в списке", 999, "boss", true},
		{"username регистронезависим", 999, "BOSS", true},
		{"username без @ в списке", 999, "second_admin", true},
		{"чужой чат", 42, "someone", false},
		{"пустой username не совпадает ни с чем", 42, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := admins.Has(c.chatID, c.username); got != c.want {
				t.Errorf("Has(%d, %q) = %v, want %v", c.chatID, c.username, got, c.want)
			}
		})
	}
}

// Пустой TELEGRAM_ADMINS должен означать «админов нет», а не «все админы».
func TestParseAdminsEmptyGrantsNothing(t *testing.T) {
	admins := ParseAdmins(nil)
	if admins.Has(123, "anyone") {
		t.Error("пустой список не должен никого пускать")
	}
	if ParseAdmins([]string{" ", ",", "@"}).Has(123, "anyone") {
		t.Error("мусорные записи не должны никого пускать")
	}
}

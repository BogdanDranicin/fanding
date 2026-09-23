package trades

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func sig(s Signal) string {
	price := ""
	if s.Price != nil {
		price = fmt.Sprintf("@%g", *s.Price)
	}
	out := fmt.Sprintf("%s %s %s %s%s", s.Action, s.Ticker, s.Direction, s.Size, price)
	if s.Stop != "" {
		out += " stop=" + s.Stop
	}
	if s.Targets != "" {
		out += " tp=" + s.Targets
	}
	if s.Book != "" {
		out += " book=" + s.Book
	}
	return strings.Join(strings.Fields(out), " ")
}

func sigs(ss []Signal) []string {
	var out []string
	for _, s := range ss {
		out = append(out, sig(s))
	}
	return out
}

func TestParseRealMessages(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		reply []string
		want  []string
	}{
		{"profitking open with book and stop", "Модельный портфель 1 Лукойл покупка 10% 5360 стоп под вчерашний минмум",
			nil, []string{"open LKOH long 10%@5360 stop=под вчерашний минмум book=МП1"}},
		{"profitking typo book, price after book", "Роснефть покупка 75% Модельный порфтель 1 362,4 Стоп на всю позицию под сегодняшинй минимум Аминь",
			nil, []string{"open ROSN long 75%@362.4 stop=на всю позицию под сегодняшинй минимум Аминь book=МП1"}},
		{"profitking thousands price", "Миксп покупка 100% 230 900 стоп под сегодняшний минимум",
			nil, []string{"open MIX long 100%@230900 stop=под сегодняшний минимум"}},
		{"profitking partial close", "Модельный портфель закрыл Лукойл 50% 🎖+2.7%",
			nil, []string{"reduce LKOH 50%"}},
		{"profitking partial close again", "Модельный портфель 2 ВТБ закрыл еще 25% 🎖+2%",
			nil, []string{"reduce VTBR 25% book=МП2"}},
		{"profitking close rest", "Модельный портфель 1 Закрыл Лукойл остаток 🎖+3%",
			nil, []string{"close LKOH book=МП1"}},
		{"profitking stop move", "Модельный портфель 1 Новатэк стоп под сегодняшний минимум",
			nil, []string{"stop NVTK stop=под сегодняшний минимум book=МП1"}},
		{"profitking two stops", "Газпром и СБербанк модельный портфель 1 стопы под сегодняшний минимум.",
			nil, []string{"stop GAZP stop=под сегодняшний минимум book=МП1", "stop SBER stop=под сегодняшний минимум book=МП1"}},
		{"commentary about a close", "Правильно, что Сбер с утра закрыл", nil, nil},
		{"commentary no verb", "Сегодня еще раз предупреждал о закрытии шортов в самолете", nil, nil},

		{"vadik qty before ticker", "6500 роснефти взял 262,95", nil, []string{"open ROSN 6500 шт@262.95"}},
		{"vadik short qty", "5000 шорта самолета взял 249,4", nil, []string{"open SMLT short 5000 шт@249.4"}},
		{"vadik lots", "10 лотов микса взял шорт", nil, []string{"open MIX short 10 лот"}},
		{"vadik long", "1500 магнита взял в лонг 1569", nil, []string{"open MGNT long 1500 шт@1569"}},
		{"vadik add to", "увеличил самолет до 6000", nil, []string{"add SMLT до 6000"}},
		{"vadik returned short", "1000 нлмк вернул шорта", nil, []string{"add NLMK short 1000 шт"}},
		{"vadik T", "Добавил Т еще", nil, []string{"add T"}},
		{"vadik close", "Закрыл самолет,псомтрю пока со стороны", nil, []string{"close SMLT"}},
		{"vadik close all shorts", "закрыл все шорты", nil, []string{"close_all short"}},
		{"vadik close all shorts inverted", "все шорты прикрыл пока", nil, []string{"close_all short"}},
		{"vadik cut with pnl", "Магнит шляпа какая то -24к порезал,на растущем рынке кремль рисует", nil, []string{"reduce MGNT"}},
		{"vadik intent", "Газик не стал по 101 брать называется, уже ниже 100", nil, nil},
		{"vadik intent 2", "Подожду че там по новостям из Вашингтона и хочу еще раз шортецкого зарядить", nil, nil},

		{"chekhov lines inherit verb", "30% ОФЗ 26248 купил\n10% $T\n3% $SVCB", nil,
			[]string{"open ОФЗ 26248 long 30%", "open T long 10%", "open SVCB long 3%"}},
		{"chekhov verb on last line", "2% $SVCB\n3% $T\nДобрал", nil, []string{"add SVCB 2%", "add T 3%"}},
		{"chekhov add up to", "$T до 14% добрал (было 7%)", nil, []string{"add T до 14%"}},
		{"chekhov avg price", "Еще 4% ОФЗ 26248 добрал, средняя 80.99.\nпока так и посижу. 14% в ОФЗ.", nil,
			[]string{"add ОФЗ 26248 4%@80.99"}},
		{"chekhov half cut", "Половину ОФЗ срезал -0.2%, выходят на аукцион с двумя фиксами", nil, []string{"reduce ОФЗ 50%"}},
		{"chekhov decided to close", "Решил в итоге всю позицию скинуть -0.35 по ОФЗ, не нравится мне", nil, []string{"close ОФЗ"}},
		{"chekhov small cut", "$T 5% прикрыл", nil, []string{"reduce T 5%"}},
		{"chekhov future", "Куплю обратно, если наша сторона подтвердит слова Трампа.", nil, nil},

		{"goodwin open short", "#UPRO\n\nОткрыл шорт по Юнипро 1,0030, стоп 1,035, профиты 0,9630 и 0,9440.", nil,
			[]string{"open UPRO short @1.003 stop=1,035 tp=0,9630 и 0,9440"}},
		{"goodwin open short gmk", "#GMKN\n\nОткрыл шорт по ГМК 128,28, стоп 130,85, профиты 125,73 и 124,08.", nil,
			[]string{"open GMKN short @128.28 stop=130,85 tp=125,73 и 124,08"}},
		{"goodwin long", "#OZON\n\nКупил Озон по 2731,5, стоп 2701, профит 2818.", nil,
			[]string{"open OZON long @2731.5 stop=2701 tp=2818"}},
		{"goodwin partial profit", "#OZON\n\nОзон профит 2818 (+3,17%)✅, остаток буду фиксировать в ручную.", nil,
			[]string{"reduce OZON @2818"}},
		{"goodwin reduce and stop to BE", "#OZON\n\nОзон сократил 2789,5 (+2,13%)✅, стоп перенёс в БУ.", nil,
			[]string{"reduce OZON @2789.5 stop=в БУ"}},
		{"goodwin close rest", "#OZON \n\nЗакрыл остаток позиции по 2874,5(+5,30%)✅", nil, []string{"close OZON @2874.5"}},
		{"goodwin closed list", "Закрытые позиции:\n\n#MIX по 229,45 (-0,39%)❌\n#NVTK по 1055,5 (-2,16%)❌\n#CHMF по 623,8 (+0,48%)✅", nil,
			[]string{"close MIX @229.45", "close NVTK @1055.5", "close CHMF @623.8"}},
		{"goodwin flat", "Ухожу совсем без позиций, крайне неудачная неделя получилась.", nil, []string{"close_all"}},
		{"goodwin news skipped", "#Новости #LKOH\nКонсорциум намерен опередить Carlyle в сделке, купил долю", nil, nil},
		{"goodwin status skipped", "Статус моих позиций на конец дня 18.09.2026.\n⬆️Лонг:\n#RBU6", nil, nil},

		{"profitgate try short market", "Пробую пошортить рынок", nil, []string{"open IMOEX short"}},
		{"profitgate short no ticker", "Попробовал небольшой шорт открыть.", nil, []string{"open ? short"}},
		{"profitgate short closed", "Шорт закрыл.. Основания закрытия будут в посте ниже.", nil, []string{"close short"}},

		{"goodwin open without verb", "#GAZP\n\nГазпром шорт 93,33, стоп 95,50, профиты 91,46 и 90,58.", nil,
			[]string{"open GAZP short @93.33 stop=95,50 tp=91,46 и 90,58"}},
		{"goodwin stopped out", "#GAZP\n\nГазпром стоп 95,60 (-1,74%)❌. Пробили поддержу, рынок сполз ниже.", nil,
			[]string{"close GAZP @95.6 stop=95,60"}},
		{"goodwin profit without verb", "#CHMF\n\nНачинаем собирать профиты. Северсталь 646 (+3,06%)✅, стоп в БУ, второй на 637.", nil,
			[]string{"reduce CHMF @646 stop=в БУ"}},
		{"goodwin close shorts by market", "Закрыл шорты по рынку, не падаем. Я предположу, что пойдем на перехай, в случае сильной нефти.", nil,
			[]string{"close_all short"}},
		{"goodwin decided close current", "Я решил закрыть текущие позиции.\n\nЧто-то никак почти не едет рынок выше.", nil, []string{"close_all"}},
		{"goodwin fixed part with next target", "#MIX\n\nЗафиксировал по миксу 60% по 228,050(+1,42%)✅, стоп в безубыток, второй профит 229,650.", nil,
			[]string{"reduce MIX 60%@228.05 stop=в безубыток tp=229,650"}},
		{"goodwin far ticker ignored", "#TATN\n\nЗакрыл Татнефть по 633,4 (-0,05%)❌ Там нефть пошла на взлет, я не буду шортить нефтянку.", nil,
			[]string{"close TATN @633.4"}},
		{"goodwin retelling skipped", "По мнению Trade, рынок слабый.\n• Trade закрыл позицию по «Газпрому»", nil, nil},
		{"chekhov would buy", "Базово купил бы дивиденд на уровне / выше 35 рублей.", nil, nil},
		{"chekhov decided with foreign percent", "Решил в итоге всю позицию скинуть -0.35 по ОФЗ, они готовы под 16% занимать", nil, []string{"close ОФЗ"}},
		{"chekhov whole cut is close", "$NVTK прикрыл с утра +1% весь, остаток позиций тяну дальше.", nil, []string{"close NVTK"}},
		{"chekhov times", "В 2 раза увеличил $GAZP под переговоры", nil, []string{"add GAZP ×2"}},
		{"chekhov each percent", "По 2% $T $SVCB взял, дошли до 2065 по ММВБ.", nil, []string{"open T 2%", "open SVCB 2%"}},
		{"chekhov foreign ticker line", "Остаток СибурХ1Р08 закрыл +2%\nДобрал 2% $SNGSP", nil, []string{"add SNGSP 2%"}},
		{"report released is not a trade", "Вчера отчет АФК вышел. СД вышла, котир начали подтягивать.", nil, nil},
		{"dump of add-ons is not close all", "По рынку скинул все докупки, которые делал в пятницу.", nil, nil},
		{"buy noun in commentary", "На рынке вряд ли будет какая то большая покупка сегодня", nil, nil},
		{"buy noun in commentary with ticker", "Внезапно откуда-то покупка в ВТБ нарисовалась в стаканах висит.", nil, nil},
		{"question skipped", "Почему я закрыл акции?", nil, nil},
		{"profit percent is not size", "Зафиксировал 5% прибыли в самолёте", nil, []string{"close SMLT"}},
		{"latin homoglyphs", "‼️купил самолeт по 330 тот что продавал по 440.", nil, []string{"open SMLT long @330"}},
		{"typo book", "Модельный портфлеь 1 Газпром покупка 25% 97,7 стоп под вчерашний минимум", nil,
			[]string{"open GAZP long 25%@97.7 stop=под вчерашний минимум book=МП1"}},
		{"oil short without verb", "Нефть шорт 10% 102, 15 стоп над максимум сегодняшнего дня.", nil,
			[]string{"open BR short 10%@102 stop=над максимум сегодняшнего дня"}},
		{"long clause without ticker ignored", "Закрыл чтобы риски не увеличились на портфель", nil, nil},
		{"reply gives ticker", "Закрыл", []string{"SBER"}, []string{"close SBER"}},
		{"headline caps skipped", "РОССИЯ РАССМОТРИТ ПРЕДЛОЖЕНИЕ УКРАИНЫ ВСТРЕТИТЬСЯ НА ГЕНАССАМБЛЕЕ ООН", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sigs(Parse(c.text, c.reply))
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("\ntext: %q\n got: %q\nwant: %q", c.text, got, c.want)
			}
		})
	}
}

func TestLedgerScenario(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	l := &Ledger{Author: "Profit King"}
	step := func(text string) []Change {
		at = at.Add(time.Minute)
		return l.Apply(Parse(text, nil), at, text)
	}

	step("Роснефть покупка 25% Модельный портфель 1 360,65")
	step("Роснефть покупка 75% Модельный порфтель 1 362,4")
	if len(l.Open) != 1 || l.Open[0].Size != "100%" || *l.Open[0].EntryPrice != 360.65 {
		t.Fatalf("после двух покупок ждали одну позицию 100%% по 360.65: %+v", l.Open)
	}

	step("Модельный портфель закрыл Роснефть 50% 🎖+2.7%")
	if l.Open[0].Size != "50%" {
		t.Fatalf("после частичного закрытия ждали 50%%: %q", l.Open[0].Size)
	}
	ch := step("Модельный портфель 1 Закрыл Роснефть остаток 🎖+3%")
	if len(l.Open) != 0 || len(ch) != 1 || ch[0].Action != ActClose {
		t.Fatalf("остаток должен закрыть позицию: open=%+v changes=%+v", l.Open, ch)
	}

	v := &Ledger{Author: "Вадик"}
	vstep := func(text string) []Change {
		at = at.Add(time.Minute)
		return v.Apply(Parse(text, nil), at, text)
	}
	vstep("5000 шорта самолета взял 249,4")
	vstep("закрыл все шорты")
	if len(v.Open) != 0 {
		t.Fatalf("закрыл все шорты: %+v", v.Open)
	}
	vstep("4000 самика вернул")
	if len(v.Open) != 1 || v.Open[0].Direction != DirShort || v.Open[0].Size != "4000 шт" {
		t.Fatalf("вернул после шорта — снова шорт: %+v", v.Open)
	}
	vstep("увеличил самолет до 6000")
	if v.Open[0].Size != "6000 шт" {
		t.Fatalf("увеличил до 6000: %q", v.Open[0].Size)
	}
	vstep("1500 магнита взял в лонг 1569")
	if ch := vstep("Шорт закрыл"); len(ch) != 1 || ch[0].Position.Ticker != "SMLT" {
		t.Fatalf("шорт без тикера при одном шорте закрывает его: %+v", ch)
	}
	if len(v.Open) != 1 || v.Open[0].Ticker != "MGNT" {
		t.Fatalf("лонг Магнита должен остаться: %+v", v.Open)
	}
	if ch := vstep("Закрыл"); len(ch) != 1 {
		t.Fatalf("единственная позиция закрывается и без тикера: %+v", ch)
	}

	g := &Ledger{Author: "Goodwin"}
	g.Apply(Parse("#OZON\nКупил Озон по 2731,5, стоп 2701, профит 2818.", nil), at, "")
	g.Apply(Parse("#OZON\nОткрыл шорт по Озону 2800", nil), at.Add(time.Minute), "")
	if len(g.Open) != 1 || g.Open[0].Direction != DirShort {
		t.Fatalf("встречная сделка переворачивает позицию: %+v", g.Open)
	}
}

func TestSizeArithmetic(t *testing.T) {
	cases := []struct{ fn func(string, string) string; a, b, want string }{
		{addSize, "25%", "75%", "100%"},
		{addSize, "7%", "до 14%", "14%"},
		{addSize, "4000 шт", "2000 шт", "6000 шт"},
		{addSize, "10 лот", "5%", "10 лот + 5%"},
		{addSize, "", "3%", "3%"},
		{subtractSize, "100%", "50%", "50%"},
		{subtractSize, "10%", "50%", "10% − 50%"},
		{subtractSize, "6000 шт", "1000 шт", "5000 шт"},
	}
	for _, c := range cases {
		if got := c.fn(c.a, c.b); got != c.want {
			t.Errorf("%q, %q: got %q want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestParseVadikQuantities(t *testing.T) {
	cases := map[string][]string{
		"Новатека 2000 взял шорт 1075,8":         {"open NVTK short 2000 шт@1075.8"},
		"2000 шорта взял GMKN 130,49":             {"open GMKN short 2000 шт@130.49"},
		"15000 шорта вк 115,58 добавил":           {"add VKCO short 15000 шт@115.58"},
		"10 лотов MXU6 взял шорт 232900":          {"open MXU6 short 10 лот@232900"},
		"увеличил самолет до 10к 270,6":           {"add SMLT до 10000@270.6"},
		"все закрыл":                              {"close_all"},
		"Новатек и норку прикрыл, Нлмк подержу":   {"reduce NVTK", "reduce GMKN"},
		"Полюс конечно зря откупил":               nil,
		"Закрыть Лонги Не Равно открыть шорты на": nil,
	}
	for text, want := range cases {
		if got := sigs(Parse(text, nil)); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("\ntext: %q\n got: %q\nwant: %q", text, got, want)
		}
	}
	long := strings.Repeat("Газпром покупка 25% 97,7. ", 80)
	if got := Parse(long, nil); got != nil {
		t.Errorf("длинный разбор должен пропускаться: %v", sigs(got))
	}
}

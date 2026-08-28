package moexiss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Справочник режима торгов разбирается по именам колонок, а не по их порядку:
// ISS отдаёт колонки в том виде, в каком их попросили, и молча меняет состав.
func TestFetchBoardSecurities(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"securities":{"columns":["SECID","SECTYPE"],` +
			`"data":[["SBER","1"],["SNGSP","2"],["AKMP","J"],["okey","D"],[null,"1"]]}}`))
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(srv.URL)
	list, err := c.FetchBoardSecurities(context.Background(), TradeFeed{
		Engine: "stock", Market: "shares", Board: "TQBR",
	})
	if err != nil {
		t.Fatalf("FetchBoardSecurities: %v", err)
	}
	if want := "/engines/stock/markets/shares/boards/TQBR/securities.json"; gotPath != want {
		t.Errorf("путь %q, хотим %q", gotPath, want)
	}
	if gotQuery == "" {
		t.Error("запрос ушёл без сужения колонок: справочник режима — это сотни строк")
	}
	if len(list) != 4 {
		t.Fatalf("бумаг %d, хотим 4 (строка без тикера отбрасывается): %+v", len(list), list)
	}
	if list[0] != (Security{SecID: "SBER", SecType: "1"}) {
		t.Errorf("первая бумага %+v", list[0])
	}
	// Тикер приводится к верхнему регистру там же, где и в ленте.
	if list[3].SecID != "OKEY" {
		t.Errorf("тикер %q, хотим OKEY", list[3].SecID)
	}
}

// Пустой справочник — это ошибка, а не «в режиме нет бумаг»: молча сузить
// наблюдение до нуля хуже, чем остаться со старым списком.
func TestFetchBoardSecuritiesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"securities":{"columns":["SECID","SECTYPE"],"data":[]}}`))
	}))
	defer srv.Close()

	_, err := NewClientWithBaseURL(srv.URL).FetchBoardSecurities(context.Background(), TradeFeed{
		Engine: "stock", Market: "shares", Board: "TQBR",
	})
	if err == nil {
		t.Fatal("пустой справочник принят за успех")
	}
}

// Борд необязателен — срочный рынок отдаёт справочник целиком, — а вот без рынка
// адресовать запрос нечем.
func TestFetchBoardSecuritiesNeedsMarket(t *testing.T) {
	_, err := NewClient().FetchBoardSecurities(context.Background(), TradeFeed{Engine: "stock"})
	if err == nil {
		t.Fatal("запрос без рынка принят")
	}
}

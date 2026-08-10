package cbr_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/funding-service/backend/internal/source/cbr"
)

// soapBody воспроизводит форму реального ответа GetCursOnDate: дата курса лежит
// в атрибуте msprop:OnDate схемы, которая идёт ПЕРЕД diffgram с самими курсами.
func soapBody(onDate, usd, eur string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
<soap:Body><GetCursOnDateResponse xmlns="http://web.cbr.ru/"><GetCursOnDateResult>
<xs:schema id="ValuteData" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:msprop="urn:schemas-microsoft-com:xml-msprop">
<xs:element name="ValuteData" msprop:OnDate=%q/>
</xs:schema>
<diffgr:diffgram xmlns:diffgr="urn:schemas-microsoft-com:xml-diffgram-v1">
<ValuteData xmlns="">
<ValuteCursOnDate><Vname>Доллар США</Vname><Vnom>1</Vnom><Vcurs>%s</Vcurs><Vcode>840</Vcode><VchCode>USD</VchCode></ValuteCursOnDate>
<ValuteCursOnDate><Vname>Евро</Vname><Vnom>1</Vnom><Vcurs>%s</Vcurs><Vcode>978</Vcode><VchCode>EUR</VchCode></ValuteCursOnDate>
</ValuteData>
</diffgr:diffgram>
</GetCursOnDateResult></GetCursOnDateResponse></soap:Body></soap:Envelope>`, onDate, usd, eur)
}

// soapClient возвращает клиент, у которого запросы к cbr.ru уезжают на тестовый
// сервер: адрес SOAP-эндпоинта в канале зашит константой.
func soapClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	return &http.Client{Transport: rewriteHost{base}}
}

type rewriteHost struct{ base *url.URL }

func (rt rewriteHost) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host, clone.Host = rt.base.Scheme, rt.base.Host, rt.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// TestFetchSOAP_UsesReturnedDateNotRequested — регрессия на баг 10.08.2026.
// До публикации ЦБ отдаёт ДЕЙСТВУЮЩИЙ курс на любую запрошенную дату, а канал
// подписывал результат датой запроса. В гонке «побеждает самая свежая дата»
// SOAP из-за этого с каждой полуночи обыгрывал официальный XML фиктивной датой,
// движок принимал это за публикацию и считал CBFunding от вчерашнего курса.
func TestFetchSOAP_UsesReturnedDateNotRequested(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		// Что бы ни просили — ЦБ отвечает действующим курсом на 08.08.2026.
		fmt.Fprint(w, soapBody("20260808", "82.1665", "94.8366"))
	}))
	defer srv.Close()

	res, err := cbr.FetchSOAP(context.Background(), soapClient(t, srv))
	if err != nil {
		t.Fatalf("FetchSOAP: %v", err)
	}
	if res.Date != "08.08.2026" {
		t.Errorf("дата должна быть та, что вернул ЦБ (08.08.2026), а не дата запроса; got %q", res.Date)
	}
	if res.USD != 82.1665 || res.EUR != 94.8366 {
		t.Errorf("курсы разъехались: USD=%v EUR=%v", res.USD, res.EUR)
	}
}

// TestFetchSOAP_PicksUpPublishedNextDayRate — обратная сторона: канал обязан
// продолжать ЛИДИРОВАТЬ публикацию. Как только запрос на завтра возвращает
// новый курс, берём его вместе с настоящей датой ЦБ.
func TestFetchSOAP_PicksUpPublishedNextDayRate(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		// Первый запрос — на сегодня, второй — на завтра (уже опубликовано).
		if atomic.AddInt32(&calls, 1) >= 2 && strings.Contains(string(body), "On_date") {
			fmt.Fprint(w, soapBody("20260811", "83.5000", "95.4000"))
			return
		}
		fmt.Fprint(w, soapBody("20260810", "82.1665", "94.8366"))
	}))
	defer srv.Close()

	res, err := cbr.FetchSOAP(context.Background(), soapClient(t, srv))
	if err != nil {
		t.Fatalf("FetchSOAP: %v", err)
	}
	if res.Date != "11.08.2026" {
		t.Errorf("после публикации ожидали дату ЦБ 11.08.2026, got %q", res.Date)
	}
	if res.USD != 83.5 {
		t.Errorf("ожидали опубликованный курс 83.5, got %v", res.USD)
	}
}

package cbr

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// ChannelResult is the normalized outcome of fetching one CBR publication channel.
// Date is "DD.MM.YYYY" (the rate's effective date); rates are per-unit RUB prices.
type ChannelResult struct {
	Date      string  // normalized DD.MM.YYYY
	USD       float64 // RUB per 1 USD
	EUR       float64 // RUB per 1 EUR
	CNY       float64 // RUB per 1 CNY
	Timestamp string  // channel-provided timestamp, if any (mirrors expose one)
}

// Channel describes one pollable CBR publication endpoint. Fetch returns the
// channel's current rates; callers compare Date against a known last date to
// detect a fresh publication. SOAP queries the next-day rate internally.
type Channel struct {
	ID   string
	Name string
	Desc string
	Fetch func(ctx context.Context, client *http.Client) (ChannelResult, error)
}

// FastChannels are the CBR-origin channels suitable for the live race: they can
// LEAD a publication. Mirrors are excluded here because they can only lag origin.
func FastChannels() []Channel {
	return []Channel{
		{"cbr_official_xml", "ЦБ РФ XML", "cbr.ru/scripts/XML_daily.asp — официальный эндпоинт (windows-1251)", FetchOfficialXML},
		{"cbr_soap", "ЦБ РФ SOAP", "cbr.ru/DailyInfoWebServ/DailyInfo.asmx — SOAP GetCursOnDate", FetchSOAP},
	}
}

// AllChannels adds the third-party mirrors to FastChannels. Used by diagnostics
// (/api/v1/cbr-race) and cbrwatch to measure the full spread, not for live emit.
func AllChannels() []Channel {
	return append(FastChannels(),
		Channel{"cbr_json_mirror", "cbr-xml-daily JSON", "cbr-xml-daily.ru/daily_json.js — зеркало с полем Timestamp", FetchJSONMirror},
		Channel{"cbr_xml_mirror", "cbr-xml-daily XML", "cbr-xml-daily.ru/daily.xml — зеркало в формате XML", FetchXMLMirror},
	)
}

// DirectClient bypasses any global proxy: CBR endpoints are reachable directly,
// exactly as the live Source's own client does it.
func DirectClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        8,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 8 * time.Second,
		},
	}
}

// ─── Official CBR XML (cbr.ru/scripts/XML_daily.asp) ─────────────────────────

func FetchOfficialXML(ctx context.Context, client *http.Client) (ChannelResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr.ru/scripts/XML_daily.asp", nil)
	if err != nil {
		return ChannelResult{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; funding-service/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return ChannelResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChannelResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return decodeValCurs(resp.Body)
}

// decodeValCurs parses the CBR ValCurs XML (windows-1251) used by the official
// endpoint and the XML mirror.
func decodeValCurs(r io.Reader) (ChannelResult, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "windows-1251") {
			return transform.NewReader(input, charmap.Windows1251.NewDecoder()), nil
		}
		return input, nil
	}
	var vc struct {
		XMLName xml.Name `xml:"ValCurs"`
		Date    string   `xml:"Date,attr"`
		Valutes []struct {
			CharCode string `xml:"CharCode"`
			Nominal  int    `xml:"Nominal"`
			Value    string `xml:"Value"`
		} `xml:"Valute"`
	}
	if err := dec.Decode(&vc); err != nil {
		return ChannelResult{}, fmt.Errorf("xml: %w", err)
	}
	res := ChannelResult{Date: normalizeDate(vc.Date)}
	for _, v := range vc.Valutes {
		p := parseValue(v.Value, v.Nominal)
		switch v.CharCode {
		case "USD":
			res.USD = p
		case "EUR":
			res.EUR = p
		case "CNY":
			res.CNY = p
		}
	}
	return res, nil
}

// ─── cbr-xml-daily.ru JSON mirror ────────────────────────────────────────────

func FetchJSONMirror(ctx context.Context, client *http.Client) (ChannelResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr-xml-daily.ru/daily_json.js", nil)
	if err != nil {
		return ChannelResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ChannelResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChannelResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var d struct {
		Date      string `json:"Date"`
		Timestamp string `json:"Timestamp"`
		Valute    map[string]struct {
			Value   float64 `json:"Value"`
			Nominal int     `json:"Nominal"`
		} `json:"Valute"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return ChannelResult{}, fmt.Errorf("json: %w", err)
	}
	nom := func(n int) float64 {
		if n <= 0 {
			return 1
		}
		return float64(n)
	}
	res := ChannelResult{Date: normalizeDate(d.Date), Timestamp: d.Timestamp}
	if v, ok := d.Valute["USD"]; ok {
		res.USD = v.Value / nom(v.Nominal)
	}
	if v, ok := d.Valute["EUR"]; ok {
		res.EUR = v.Value / nom(v.Nominal)
	}
	if v, ok := d.Valute["CNY"]; ok {
		res.CNY = v.Value / nom(v.Nominal)
	}
	return res, nil
}

// ─── cbr-xml-daily.ru XML mirror ─────────────────────────────────────────────

func FetchXMLMirror(ctx context.Context, client *http.Client) (ChannelResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr-xml-daily.ru/daily.xml", nil)
	if err != nil {
		return ChannelResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ChannelResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChannelResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return decodeValCurs(resp.Body)
}

// ─── CBR SOAP (DailyInfoWebServ/DailyInfo.asmx) ──────────────────────────────

// FetchSOAP returns the freshest rates the SOAP service exposes. The CBR
// publishes next-day rates around 16:30 MSK, so we request tomorrow and accept
// it only when USD differs from today (otherwise the service echoed today's
// values for a future date — pre-publication behaviour).
func FetchSOAP(ctx context.Context, client *http.Client) (ChannelResult, error) {
	msk := time.FixedZone("MSK", mskOffset)
	now := time.Now().In(msk)

	today, err := soapOnDate(ctx, client, now)
	if err != nil || today.USD == 0 {
		return today, err
	}
	tomorrow := now.AddDate(0, 0, 1)
	tmr, errTmr := soapOnDate(ctx, client, tomorrow)
	if errTmr == nil && tmr.USD != 0 && tmr.USD != today.USD {
		return tmr, nil
	}
	return today, nil
}

func soapOnDate(ctx context.Context, client *http.Client, date time.Time) (ChannelResult, error) {
	envelope := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>`+
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:web="http://web.cbr.ru/">`+
		`<soap:Body><web:GetCursOnDate><web:On_date>%s</web:On_date></web:GetCursOnDate></soap:Body>`+
		`</soap:Envelope>`, date.Format("2006-01-02T00:00:00"))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.cbr.ru/DailyInfoWebServ/DailyInfo.asmx",
		strings.NewReader(envelope))
	if err != nil {
		return ChannelResult{}, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"http://web.cbr.ru/GetCursOnDate"`)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; funding-service/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return ChannelResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChannelResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChannelResult{}, err
	}

	type valuteCursOnDate struct {
		VchCode string  `xml:"VchCode"`
		VCurs   float64 `xml:"Vcurs"`
		VNom    float64 `xml:"Vnom"`
	}
	type diffgram struct {
		Valutes []valuteCursOnDate `xml:"ValuteData>ValuteCursOnDate"`
	}

	dec := xml.NewDecoder(bytes.NewReader(body))
	var dg diffgram
	for {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "diffgram" {
			continue
		}
		if err = dec.DecodeElement(&dg, &se); err != nil {
			return ChannelResult{}, fmt.Errorf("soap diffgram: %w", err)
		}
		break
	}
	if len(dg.Valutes) == 0 {
		return ChannelResult{}, fmt.Errorf("нет данных за %s", date.Format("02.01.2006"))
	}

	res := ChannelResult{Date: date.Format("02.01.2006")}
	for _, v := range dg.Valutes {
		nom := v.VNom
		if nom <= 0 {
			nom = 1
		}
		price := v.VCurs / nom
		switch strings.TrimSpace(v.VchCode) {
		case "USD":
			res.USD = price
		case "EUR":
			res.EUR = price
		case "CNY":
			res.CNY = price
		}
	}
	return res, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// normalizeDate converts ISO "2025-05-26T…" to "26.05.2025"; leaves DD.MM.YYYY as-is.
func normalizeDate(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[8:10] + "." + s[5:7] + "." + s[0:4]
	}
	return s
}

// parseValue parses a comma-decimal string and divides by nominal.
func parseValue(s string, nominal int) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v == 0 {
		return 0
	}
	if nominal > 1 {
		return v / float64(nominal)
	}
	return v
}

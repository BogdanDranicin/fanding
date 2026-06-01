package api

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// CBRRaceResult holds one source's outcome in the CBR rate-publication race.
type CBRRaceResult struct {
	SourceID  string  `json:"source_id"`
	Name      string  `json:"name"`
	Desc      string  `json:"desc"`
	RateDate  string  `json:"rate_date"`
	Timestamp string  `json:"timestamp,omitempty"`
	USDRUB    float64 `json:"usd_rub"`
	EURRUB    float64 `json:"eur_rub"`
	CNYRUB    float64 `json:"cny_rub"`
	LatencyMs int64   `json:"latency_ms"`
	Error     string  `json:"error,omitempty"`
}

type cbrRaceSource struct {
	id   string
	name string
	desc string
	fn   func(ctx context.Context, client *http.Client) (CBRRaceResult, error)
}

// cbrDirectClient bypasses the global proxy (HTTPS_PROXY env) used by globalAllSpecs.httpClient.
// CBR endpoints are accessible directly from the container, just like cbr/source.go does it.
var cbrDirectClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        5,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 8 * time.Second,
	},
}

var cbrRaceSources = []cbrRaceSource{
	{
		"cbr_official_xml",
		"ЦБ РФ XML",
		"cbr.ru/scripts/XML_daily.asp — официальный эндпоинт (windows-1251)",
		fetchCBROfficialXML,
	},
	{
		"cbr_json_mirror",
		"cbr-xml-daily JSON",
		"cbr-xml-daily.ru/daily_json.js — зеркало с полем Timestamp",
		fetchCBRJSONMirror,
	},
	{
		"cbr_xml_mirror",
		"cbr-xml-daily XML",
		"cbr-xml-daily.ru/daily.xml — зеркало в формате XML",
		fetchCBRXMLMirror,
	},
	{
		"cbr_soap",
		"ЦБ РФ SOAP",
		"cbr.ru/DailyInfoWebServ/DailyInfo.asmx — SOAP GetCursOnDate",
		fetchCBRSOAP,
	},
}

func handleCBRRace(w http.ResponseWriter, r *http.Request) {
	client := cbrDirectClient
	resCh := make(chan CBRRaceResult, len(cbrRaceSources))

	var wg sync.WaitGroup
	for _, src := range cbrRaceSources {
		wg.Add(1)
		go func(src cbrRaceSource) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()

			start := time.Now()
			res, err := src.fn(ctx, client)
			res.SourceID = src.id
			res.Name = src.name
			res.Desc = src.desc
			res.LatencyMs = time.Since(start).Milliseconds()
			if err != nil {
				res.Error = err.Error()
			}
			resCh <- res
		}(src)
	}

	go func() { wg.Wait(); close(resCh) }()

	results := make([]CBRRaceResult, 0, len(cbrRaceSources))
	for res := range resCh {
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].LatencyMs < results[j].LatencyMs
	})

	writeJSON(w, http.StatusOK, results)
}

// ─── Official CBR XML (cbr.ru/scripts/XML_daily.asp) ─────────────────────────

func fetchCBROfficialXML(ctx context.Context, client *http.Client) (CBRRaceResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr.ru/scripts/XML_daily.asp", nil)
	if err != nil {
		return CBRRaceResult{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; funding-service/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return CBRRaceResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CBRRaceResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return decodeCBRValCurs(resp.Body)
}

func decodeCBRValCurs(r io.Reader) (CBRRaceResult, error) {
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
		return CBRRaceResult{}, fmt.Errorf("xml: %w", err)
	}
	res := CBRRaceResult{RateDate: normalizeCBRDate(vc.Date)}
	for _, v := range vc.Valutes {
		p := parseCBRValue(v.Value, v.Nominal)
		switch v.CharCode {
		case "USD":
			res.USDRUB = p
		case "EUR":
			res.EURRUB = p
		case "CNY":
			res.CNYRUB = p
		}
	}
	return res, nil
}

// ─── cbr-xml-daily.ru JSON mirror ────────────────────────────────────────────

func fetchCBRJSONMirror(ctx context.Context, client *http.Client) (CBRRaceResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr-xml-daily.ru/daily_json.js", nil)
	if err != nil {
		return CBRRaceResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return CBRRaceResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CBRRaceResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
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
		return CBRRaceResult{}, fmt.Errorf("json: %w", err)
	}
	nom := func(n int) float64 {
		if n <= 0 {
			return 1
		}
		return float64(n)
	}
	res := CBRRaceResult{RateDate: normalizeCBRDate(d.Date), Timestamp: d.Timestamp}
	if v, ok := d.Valute["USD"]; ok {
		res.USDRUB = v.Value / nom(v.Nominal)
	}
	if v, ok := d.Valute["EUR"]; ok {
		res.EURRUB = v.Value / nom(v.Nominal)
	}
	if v, ok := d.Valute["CNY"]; ok {
		res.CNYRUB = v.Value / nom(v.Nominal)
	}
	return res, nil
}

// ─── cbr-xml-daily.ru XML mirror ─────────────────────────────────────────────

func fetchCBRXMLMirror(ctx context.Context, client *http.Client) (CBRRaceResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr-xml-daily.ru/daily.xml", nil)
	if err != nil {
		return CBRRaceResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return CBRRaceResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CBRRaceResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return decodeCBRValCurs(resp.Body)
}

// ─── CBR SOAP (DailyInfoWebServ/DailyInfo.asmx) ──────────────────────────────

func fetchCBRSOAP(ctx context.Context, client *http.Client) (CBRRaceResult, error) {
	msk := time.FixedZone("MSK", 3*60*60)
	now := time.Now().In(msk)

	// Always fetch today's rates first as the baseline.
	today, err := doSOAPRequest(ctx, client, now)
	if err != nil || today.USDRUB == 0 {
		return today, err
	}

	// Try tomorrow: ЦБ РФ publishes next-day rates around 16:30 MSK.
	// We only accept tomorrow's response if the USD rate actually differs
	// from today — otherwise the endpoint returned today's data for a
	// future date (pre-publication behaviour), which would be a false positive.
	tomorrow := now.AddDate(0, 0, 1)
	tmr, errTmr := doSOAPRequest(ctx, client, tomorrow)
	if errTmr == nil && tmr.USDRUB != 0 && tmr.USDRUB != today.USDRUB {
		return tmr, nil
	}

	return today, nil
}

func doSOAPRequest(ctx context.Context, client *http.Client, date time.Time) (CBRRaceResult, error) {
	envelope := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>`+
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:web="http://web.cbr.ru/">`+
		`<soap:Body><web:GetCursOnDate><web:On_date>%s</web:On_date></web:GetCursOnDate></soap:Body>`+
		`</soap:Envelope>`, date.Format("2006-01-02T00:00:00"))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.cbr.ru/DailyInfoWebServ/DailyInfo.asmx",
		strings.NewReader(envelope))
	if err != nil {
		return CBRRaceResult{}, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"http://web.cbr.ru/GetCursOnDate"`)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; funding-service/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return CBRRaceResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CBRRaceResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CBRRaceResult{}, err
	}

	// CBR SOAP DataSet: root element is "ValuteData", records are "ValuteCursOnDate"
	type ValuteCursOnDate struct {
		VchCode string  `xml:"VchCode"`
		VCurs   float64 `xml:"Vcurs"`
		VNom    float64 `xml:"Vnom"`
	}
	type diffgram struct {
		Valutes []ValuteCursOnDate `xml:"ValuteData>ValuteCursOnDate"`
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
			return CBRRaceResult{}, fmt.Errorf("soap diffgram: %w", err)
		}
		break
	}
	if len(dg.Valutes) == 0 {
		return CBRRaceResult{}, fmt.Errorf("нет данных за %s", date.Format("02.01.2006"))
	}

	res := CBRRaceResult{RateDate: date.Format("02.01.2006")}
	for _, v := range dg.Valutes {
		nom := v.VNom
		if nom <= 0 {
			nom = 1
		}
		price := v.VCurs / nom
		switch strings.TrimSpace(v.VchCode) {
		case "USD":
			res.USDRUB = price
		case "EUR":
			res.EURRUB = price
		case "CNY":
			res.CNYRUB = price
		}
	}
	return res, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// normalizeCBRDate converts ISO "2025-05-26T…" to "26.05.2025"; leaves DD.MM.YYYY as-is.
func normalizeCBRDate(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[8:10] + "." + s[5:7] + "." + s[0:4]
	}
	return s
}

// parseCBRValue parses a comma-decimal string and divides by nominal.
func parseCBRValue(s string, nominal int) float64 {
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

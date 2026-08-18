// Package tinvest подключается к T-Invest API за лентой обезличенных сделок.
//
// Зачем он нужен рядом с уже имеющимся MOEX ISS: публичная лента биржи приходит
// с задержкой ровно в пятнадцать минут (замер 18.08.2026 — 902 секунды по SBER
// против 51 секунды у того же инструмента здесь). Для поиска роботов, где важна
// свежесть принта, это потолок, который не обойти ничем, кроме другого источника.
//
// Пакет намеренно не зависит от остального сервиса: он отдаёт свои Trade, а
// перекладывание их в принты детектора делает вызывающий.
package tinvest

import (
	_ "embed"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
)

// DefaultEndpoint — боевой адрес gRPC API.
const DefaultEndpoint = "invest-public-api.tinkoff.ru:443"

// russianTrustedRootCA — корневой сертификат НУЦ Минцифры, которым подписан
// сертификат API. В стандартных хранилищах доверия его нет, поэтому носим с собой:
// иначе соединение падает с «untrusted root» и на голом образе, и на машине
// разработчика. Сертификат публичный, ничего секретного в нём нет.
//
//go:embed russian_trusted_root_ca.pem
var russianTrustedRootCA []byte

// Client — соединение с T-Invest API.
type Client struct {
	conn   *grpc.ClientConn
	token  string
	market pb.MarketDataStreamServiceClient
	instr  pb.InstrumentsServiceClient
}

// Config — что нужно для подключения.
type Config struct {
	// Token — токен доступа из личного кабинета. Достаточно read-only.
	Token string
	// Endpoint — адрес API; пусто означает боевой.
	Endpoint string
	// AppName попадает в метаданные запросов и помогает бирже разбирать нагрузку.
	AppName string
}

// Dial устанавливает соединение. Возвращённый клиент нужно закрыть.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("tinvest: пустой токен")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(russianTrustedRootCA) {
		return nil, errors.New("tinvest: не разобрал вшитый корневой сертификат")
	}
	// Доверие расширяем только для этого соединения, а не для всего процесса:
	// системное хранилище трогать ради одного хоста незачем.
	creds := credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})

	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(creds),
		grpc.WithUnaryInterceptor(authUnary(cfg)),
		grpc.WithStreamInterceptor(authStream(cfg)),
	)
	if err != nil {
		return nil, fmt.Errorf("tinvest: подключение: %w", err)
	}
	return &Client{
		conn:   conn,
		token:  cfg.Token,
		market: pb.NewMarketDataStreamServiceClient(conn),
		instr:  pb.NewInstrumentsServiceClient(conn),
	}, nil
}

// Close закрывает соединение.
func (c *Client) Close() error { return c.conn.Close() }

// authContext добавляет к запросу токен и имя приложения.
func authContext(ctx context.Context, cfg Config) context.Context {
	md := metadata.New(map[string]string{"Authorization": "Bearer " + cfg.Token})
	if cfg.AppName != "" {
		md.Set("x-app-name", cfg.AppName)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func authUnary(cfg Config) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(authContext(ctx, cfg), method, req, reply, cc, opts...)
	}
}

func authStream(cfg Config) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(authContext(ctx, cfg), desc, cc, method, opts...)
	}
}

// Instrument — инструмент из каталога брокера.
type Instrument struct {
	// Ticker совпадает с SECID биржи: у акций это SBER, у срочных контрактов —
	// короткий код вроде MXU6 (то же самое приходит в ленте ISS).
	Ticker string
	UID    string
	// ClassCode — TQBR у акций основного режима, SPBFUT у срочного рынка.
	ClassCode string
}

// Instruments отдаёт акции основного режима и фьючерсы одним списком.
//
// Подписка в стриме идёт по UID инструмента, а весь остальной сервис оперирует
// тикерами биржи, поэтому соответствие приходится держать у себя.
func (c *Client) Instruments(ctx context.Context) ([]Instrument, error) {
	out := make([]Instrument, 0, 2048)

	shares, err := c.instr.Shares(ctx, &pb.InstrumentsRequest{
		InstrumentStatus: pb.InstrumentStatus_INSTRUMENT_STATUS_BASE.Enum(),
	})
	if err != nil {
		return nil, fmt.Errorf("tinvest: список акций: %w", err)
	}
	for _, s := range shares.GetInstruments() {
		out = append(out, Instrument{Ticker: s.GetTicker(), UID: s.GetUid(), ClassCode: s.GetClassCode()})
	}

	futures, err := c.instr.Futures(ctx, &pb.InstrumentsRequest{
		InstrumentStatus: pb.InstrumentStatus_INSTRUMENT_STATUS_BASE.Enum(),
	})
	if err != nil {
		return nil, fmt.Errorf("tinvest: список фьючерсов: %w", err)
	}
	for _, f := range futures.GetInstruments() {
		out = append(out, Instrument{Ticker: f.GetTicker(), UID: f.GetUid(), ClassCode: f.GetClassCode()})
	}
	return out, nil
}

// Trade — обезличенная сделка из стрима.
type Trade struct {
	// Ticker — тикер биржи (SECID), уже переведённый из UID инструмента.
	Ticker string
	// Time — биржевое время сделки. В отличие от ленты ISS, приходит точнее секунды.
	Time time.Time
	// Price — цена за один инструмент, Qty — количество лотов.
	Price float64
	Qty   float64
	// Buy — агрессором был покупатель.
	Buy bool
}

// quotationToFloat переводит биржевую котировку (целая часть плюс нанокопейки)
// в обычное число.
func quotationToFloat(q *pb.Quotation) float64 {
	if q == nil {
		return 0
	}
	return float64(q.GetUnits()) + float64(q.GetNano())/1e9
}

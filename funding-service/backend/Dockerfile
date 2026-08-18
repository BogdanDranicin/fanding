FROM golang:1.26-alpine AS builder
# git нужен сборке модулей: не всякий модуль отдаётся кэшем proxy.golang.org, и
# на таких Go уходит забирать исходники прямо из репозитория. Без git это падает
# с «unable to resolve git version», причём только на тех модулях, которых нет
# в кэше, — поэтому раньше сборка проходила.
RUN apk --no-cache add git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/server ./cmd/server

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
RUN adduser -D -u 10001 app
COPY --from=builder /bin/server /bin/server
USER app
ENTRYPOINT ["/bin/server"]

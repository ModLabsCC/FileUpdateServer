FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o fileupdateserver

FROM alpine:3.20

WORKDIR /app
COPY --from=builder /app/fileupdateserver .
COPY templates ./templates

RUN mkdir -p data

ENV FILE_UPLOAD_PASSWORD=
EXPOSE 8080

CMD ["./fileupdateserver"]



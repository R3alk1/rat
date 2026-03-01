FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /bin/c2 ./cmd/c2
RUN go build -o /bin/mgmt ./cmd/mgmt

FROM alpine:latest AS c2
WORKDIR /root/
COPY --from=builder /bin/c2 .
EXPOSE 8081 50051
CMD ["./c2"]

FROM alpine:latest AS mgmt
WORKDIR /root/
COPY --from=builder /bin/mgmt .
COPY --from=builder /app/web ./web
EXPOSE 8080
CMD ["./mgmt"]
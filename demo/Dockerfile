FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -o /bin/proxy ./cmd/proxy && \
    CGO_ENABLED=0 go build -o /bin/provider ./cmd/provider && \
    CGO_ENABLED=0 go build -o /bin/mockllm ./cmd/mockllm

FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/proxy /bin/provider /bin/mockllm /usr/local/bin/
COPY demo/demo.yaml /etc/infermesh/demo.yaml

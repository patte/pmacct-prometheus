FROM golang:1.16 AS go-builder
WORKDIR /go/src/app
COPY *.go .
COPY *.mod .
COPY *.sum .
RUN go mod download
RUN go build -o pmacct-prometheus

FROM pmacct/pmacctd:latest
WORKDIR /app
COPY --from=go-builder /go/src/app/pmacct-prometheus .
ENTRYPOINT ["/app/pmacct-prometheus"]

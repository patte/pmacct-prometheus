FROM golang:1.16 AS go-builder
WORKDIR /go/src/app
COPY ./ .
RUN go mod download
RUN go build -o pmacct-prometheus

FROM pmacct/pmacctd:latest
WORKDIR /app
COPY --from=go-builder /go/src/app/pmacct-prometheus .
COPY --from=go-builder /go/src/app/GeoLite2-City.mmdb .
COPY --from=go-builder /go/src/app/GeoLite2-ASN.mmdb .
ENTRYPOINT ["/app/pmacct-prometheus"]

FROM alpine:latest

RUN apk --no-cache add ca-certificates

COPY dist/jagr-gateway-linux-amd64 /usr/local/bin/jagr-gateway

RUN chmod +x /usr/local/bin/jagr-gateway

EXPOSE 8443

ENTRYPOINT ["/usr/local/bin/jagr-gateway"]

CMD ["-config", "/etc/jagr/gateway.yaml"]

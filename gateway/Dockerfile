FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY dist/jagr-gateway-linux-amd64 /usr/local/bin/jagr-gateway

RUN chmod +x /usr/local/bin/jagr-gateway

EXPOSE 8443
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/jagr-gateway"]

CMD ["--config", "/jagr/gateway.yaml"]

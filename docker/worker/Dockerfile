FROM golang:1.25-alpine

COPY bin/worker /usr/bin/worker
RUN chmod +x /usr/bin/worker

WORKDIR /

CMD ["worker"]

FROM golang:1.25-alpine

COPY bin/server /usr/bin/server
RUN chmod +x /usr/bin/server

WORKDIR /

CMD ["server"]

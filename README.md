# coturn_exporter

This is a rudimentary exporter for coturn stats using the redis statsdb.

## Building

```
CGO_ENABLED=0 go build -o coturn_exporter main.go
```

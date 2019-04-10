// Copyright 2019 DMM.com
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iknow/coturn_exporter/histogauge"

	"github.com/go-redis/redis"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	metricRegexp, _ = regexp.Compile("rcvp=([0-9]+), rcvb=([0-9]+), sentp=([0-9]+), sentb=([0-9]+)")
	keyRegexp, _    = regexp.Compile("(turn/realm/([^/]+)/user/[^/]+/allocation/[^/]+)/(.+)")
)

var (
	metricLabels = []string{"realm"}

	// 16K, 32K, 64K, 128K, 256K, 512K, 1M, 2M
	byteRateBuckets = prometheus.ExponentialBuckets(16384, 2, 8)
	// 50, 100, 150, 200, 250, 300, 350, 400
	packetRateBuckets = prometheus.LinearBuckets(50, 50, 8)
)

var (
	listenAddress = flag.String("listen-address", ":8080", "The address to listen on for HTTP requests.")
	redisUrl      = flag.String("redis-url", "redis://127.0.0.1:6379", "The redis server used as the coturn statsdb.")
)

var (
	allocationGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "coturn_allocations",
		Help: "Number of allocations",
	}, metricLabels)
	receivedPackets = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "coturn_received_packets_total",
		Help: "Number of packets received",
	}, metricLabels)
	receivedBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "coturn_received_bytes_total",
		Help: "Number of bytes received",
	}, metricLabels)
	sentPackets = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "coturn_sent_packets_total",
		Help: "Number of packets sent",
	}, metricLabels)
	sentBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "coturn_sent_bytes_total",
		Help: "Number of bytes sent",
	}, metricLabels)
	receivedPacketRateHistogauge = histogauge.NewHistogauge(prometheus.GaugeOpts{
		Name: "coturn_received_packet_rate_pps_bucket",
		Help: "Received packet rate distribution",
	}, metricLabels, packetRateBuckets)
	receivedByteRateHistogauge = histogauge.NewHistogauge(prometheus.GaugeOpts{
		Name: "coturn_received_byte_rate_bps_bucket",
		Help: "Received byte rate distribution",
	}, metricLabels, byteRateBuckets)
	sentPacketRateHistogauge = histogauge.NewHistogauge(prometheus.GaugeOpts{
		Name: "coturn_sent_packet_rate_pps_bucket",
		Help: "Sent packet rate distribution",
	}, metricLabels, packetRateBuckets)
	sentByteRateHistogauge = histogauge.NewHistogauge(prometheus.GaugeOpts{
		Name: "coturn_sent_byte_rate_bps_bucket",
		Help: "Sent byte rate distribution",
	}, metricLabels, byteRateBuckets)
)

func init() {
	prometheus.MustRegister(allocationGauge)
	prometheus.MustRegister(receivedPackets)
	prometheus.MustRegister(receivedBytes)
	prometheus.MustRegister(sentPackets)
	prometheus.MustRegister(sentBytes)
	prometheus.MustRegister(receivedPacketRateHistogauge.GaugeVec())
	prometheus.MustRegister(receivedByteRateHistogauge.GaugeVec())
	prometheus.MustRegister(sentPacketRateHistogauge.GaugeVec())
	prometheus.MustRegister(sentByteRateHistogauge.GaugeVec())
}

var allocations = make(map[string]*Allocation)

type MessageMetadata struct {
	realm          string
	allocationName string
	messageType    string
}

type TrafficMetric struct {
	rcvp  float64
	rcvb  float64
	sentp float64
	sentb float64
}

type Allocation struct {
	previousRates       *TrafficMetric
	lastMetricTimestamp time.Time
}

func parseKeyName(key string) (MessageMetadata, error) {
	var metadata MessageMetadata

	result := keyRegexp.FindStringSubmatch(key)
	if result == nil {
		return metadata, errors.New("Unexpected key name")
	}

	metadata = MessageMetadata{
		result[2],
		result[1],
		result[3],
	}
	return metadata, nil
}

func parseTrafficMetric(data string) (TrafficMetric, error) {
	var trafficMetric TrafficMetric
	result := metricRegexp.FindStringSubmatch(data)
	if result == nil {
		return trafficMetric, errors.New("Unexpected traffic metric")
	}

	rcvp, _ := strconv.ParseFloat(result[1], 64)
	rcvb, _ := strconv.ParseFloat(result[2], 64)
	sentp, _ := strconv.ParseFloat(result[3], 64)
	sentb, _ := strconv.ParseFloat(result[4], 64)

	trafficMetric = TrafficMetric{rcvp, rcvb, sentp, sentb}
	return trafficMetric, nil
}

func watchTraffic(client *redis.Client) {
	subscription := client.PSubscribe("turn/realm/*/user/*/allocation/*/*")
	channel := subscription.Channel()

	for {
		msg := <-channel

		metadata, err := parseKeyName(msg.Channel)
		if err != nil {
			fmt.Println("Unexpected key name: %s", msg.Channel)
			continue
		}
		labels := prometheus.Labels{"realm": metadata.realm}

		if metadata.messageType == "traffic" {
			trafficMetric, err := parseTrafficMetric(msg.Payload)
			if err != nil {
				fmt.Println("Unexpected traffic payload: %s", msg.Payload)
				continue
			}

			receivedPackets.With(labels).Add(trafficMetric.rcvp)
			receivedBytes.With(labels).Add(trafficMetric.rcvb)
			sentPackets.With(labels).Add(trafficMetric.sentp)
			sentBytes.With(labels).Add(trafficMetric.sentb)

			allocation := allocations[metadata.allocationName]
			if allocation != nil {
				now := time.Now()
				elapsed := now.Sub(allocation.lastMetricTimestamp).Seconds()
				rcvp_rate := trafficMetric.rcvp / elapsed
				rcvb_rate := trafficMetric.rcvb / elapsed
				sentp_rate := trafficMetric.sentp / elapsed
				sentb_rate := trafficMetric.sentb / elapsed
				rates := TrafficMetric{rcvp_rate, rcvb_rate, sentp_rate, sentb_rate}

				if allocation.previousRates != nil {
					receivedPacketRateHistogauge.Replace(labels, rcvp_rate, allocation.previousRates.rcvp)
					receivedByteRateHistogauge.Replace(labels, rcvb_rate, allocation.previousRates.rcvb)
					sentPacketRateHistogauge.Replace(labels, sentp_rate, allocation.previousRates.sentp)
					sentByteRateHistogauge.Replace(labels, sentb_rate, allocation.previousRates.sentb)
				} else {
					receivedPacketRateHistogauge.Add(labels, rcvp_rate)
					receivedByteRateHistogauge.Add(labels, rcvb_rate)
					sentPacketRateHistogauge.Add(labels, sentp_rate)
					sentByteRateHistogauge.Add(labels, sentb_rate)
				}

				allocations[metadata.allocationName] = &Allocation{&rates, time.Now()}
			} else {
				allocations[metadata.allocationName] = &Allocation{nil, time.Now()}
			}
		} else if metadata.messageType == "status" {
			if strings.HasPrefix(msg.Payload, "new") {
				allocationGauge.With(labels).Inc()
			} else if msg.Payload == "deleted" {
				allocationGauge.With(labels).Dec()
				allocation := allocations[metadata.allocationName]
				if allocation != nil {
					if allocation.previousRates != nil {
						receivedPacketRateHistogauge.Remove(labels, allocation.previousRates.rcvp)
						receivedByteRateHistogauge.Remove(labels, allocation.previousRates.rcvb)
						sentPacketRateHistogauge.Remove(labels, allocation.previousRates.sentp)
						sentByteRateHistogauge.Remove(labels, allocation.previousRates.sentb)
					}
					delete(allocations, metadata.allocationName)
				}
			}
		}
	}
}

func main() {
	flag.Parse()
	opt, err := redis.ParseURL(*redisUrl)
	if err != nil {
		panic(err)
	}
	client := redis.NewClient(opt)

	// initialize allocation gauge
	fmt.Println("Initializing allocation count")
	keys, err := client.Keys("turn/realm/*/user/*/allocation/*/status").Result()
	if err != nil {
		panic(err)
	}
	for _, key := range keys {
		metadata, err := parseKeyName(key)
		if err != nil {
			fmt.Println("Unexpected key name: %s", key)
			continue
		}
		allocationGauge.With(prometheus.Labels{"realm": metadata.realm}).Inc()
	}

	// watch for pubsub traffic events
	fmt.Println("Watching traffic")
	go watchTraffic(client)

	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}

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

package histogauge

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
)

// At the moment, this is more a hack than a proper abstraction. It does not
// implement the proper metric interface and is just a shim on top of
// GaugeVec.
type Histogauge interface {
	GaugeVec() *prometheus.GaugeVec
	Add(prometheus.Labels, float64)
	Remove(prometheus.Labels, float64)
	Replace(prometheus.Labels, float64, float64)
}

type histogauge struct {
	gaugeVec *prometheus.GaugeVec
	buckets  []float64
}

func NewHistogauge(opts prometheus.GaugeOpts, labelNames []string, buckets []float64) Histogauge {
	return &histogauge{
		gaugeVec: prometheus.NewGaugeVec(opts, append(labelNames, "le")),
		buckets:  buckets,
	}
}

func bucketName(v float64) string {
	return fmt.Sprintf("%g", v)
}

func (h *histogauge) GaugeVec() *prometheus.GaugeVec {
	return h.gaugeVec
}

func (h *histogauge) Add(labels prometheus.Labels, v float64) {
	newLabels := prometheus.Labels{}
	for k, v := range labels {
		newLabels[k] = v
	}

	for _, bucket := range h.buckets {
		newLabels["le"] = bucketName(bucket)
		// this ensures the bucket is initialized
		gauge := h.gaugeVec.With(newLabels)
		if v <= bucket {
			gauge.Inc()
		}
	}
	newLabels["le"] = "+Inf"
	h.gaugeVec.With(newLabels).Inc()
}

func (h *histogauge) Remove(labels prometheus.Labels, v float64) {
	newLabels := prometheus.Labels{}
	for k, v := range labels {
		newLabels[k] = v
	}

	for _, bucket := range h.buckets {
		newLabels["le"] = bucketName(bucket)
		// this ensures the bucket is initialized
		gauge := h.gaugeVec.With(newLabels)
		if v <= bucket {
			gauge.Dec()
		}
	}
	newLabels["le"] = "+Inf"
	h.gaugeVec.With(newLabels).Dec()
}

func (h *histogauge) Replace(labels prometheus.Labels, v float64, o float64) {
	if v == o {
		return
	}

	newLabels := prometheus.Labels{}
	for k, v := range labels {
		newLabels[k] = v
	}

	for _, bucket := range h.buckets {
		newLabels["le"] = bucketName(bucket)
		if v > o {
			if o <= bucket && bucket < v {
				h.gaugeVec.With(newLabels).Dec()
			}
		} else {
			if v <= bucket && bucket < o {
				h.gaugeVec.With(newLabels).Inc()
			}
		}
	}
}

// SPDX-FileCopyrightText: 2021 - 2023 Iván Szkiba
//
// SPDX-License-Identifier: MIT

package internal

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"go.k6.io/k6/metrics"
)

type PrometheusAdapter struct {
	Subsystem string
	Namespace string
	logger    logrus.FieldLogger
	metrics   map[string]interface{}
	registry  *prometheus.Registry
}

type labelNames []string

type counterWithLabels struct {
	counterVec *prometheus.CounterVec
	labelNames labelNames
}

type gaugeWithLabels struct {
	gaugeVec   *prometheus.GaugeVec
	labelNames labelNames
}

type summaryWithLabels struct {
	summaryVec *prometheus.SummaryVec
	labelNames labelNames
}

type histogramWithLabels struct {
	histogramVec *prometheus.HistogramVec
	labelNames   labelNames
}

func NewPrometheusAdapter(registry *prometheus.Registry, logger logrus.FieldLogger, ns, sub string) *PrometheusAdapter {
	return &PrometheusAdapter{
		Subsystem: sub,
		Namespace: ns,
		logger:    logger,
		registry:  registry,
		metrics:   make(map[string]interface{}),
	}
}

func (a *PrometheusAdapter) AddMetricSamples(samples []metrics.SampleContainer) {
	for i := range samples {
		all := samples[i].GetSamples()
		for j := range all {
			a.handleSample(&all[j])
		}
	}
}

func (a *PrometheusAdapter) Handler() http.Handler {
	return promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}) // nolint:exhaustruct
}

func (a *PrometheusAdapter) handleSample(sample *metrics.Sample) {
	var handler func(*metrics.Sample)

	switch sample.Metric.Type {
	case metrics.Counter:
		handler = a.handleCounter
	case metrics.Gauge:
		handler = a.handleGauge
	case metrics.Rate:
		handler = a.handleRate
	case metrics.Trend:
		handler = a.handleTrend
	default:
		a.logger.Warnf("Unknown metric type: %v", sample.Metric.Type)

		return
	}

	handler(sample)
}

func (a *PrometheusAdapter) tagsToLabelNames(tags *metrics.TagSet) []string {
	m := tags.Map()
	m["tls_version"] = "" // created later by k6

	keys := make([]string, 0, len(m))

	for key := range m {
		keys = append(keys, key)
	}

	return keys
}

func (a *PrometheusAdapter) tagsToLabelValues(labelNames []string, sampleTags *metrics.TagSet) []string {
	tags := sampleTags.Map()
	labelValues := []string{}

	for _, label := range labelNames {
		labelValues = append(labelValues, tags[label])
		delete(tags, label)
	}

	if len(tags) > 0 {
		a.logger.WithField("unused_tags", tags).Warn("Not all tags used as labels")
	}

	return labelValues
}

func (a *PrometheusAdapter) handleCounter(sample *metrics.Sample) {
	if counter := a.getCounter(sample.Metric.Name, "k6 counter", sample.Tags); counter != nil {
		labelValues := a.tagsToLabelValues(counter.labelNames, sample.Tags)
		metric, err := counter.counterVec.GetMetricWithLabelValues(labelValues...)

		if err != nil {
			a.logger.Error(err)
		} else {
			metric.Add(sample.Value)
		}
	}
}

func (a *PrometheusAdapter) handleGauge(sample *metrics.Sample) {
	if gauge := a.getGauge(sample.Metric.Name, "k6 gauge", sample.Tags); gauge != nil {
		labelValues := a.tagsToLabelValues(gauge.labelNames, sample.Tags)
		metric, err := gauge.gaugeVec.GetMetricWithLabelValues(labelValues...)

		if err != nil {
			a.logger.Error(err)
		} else {
			metric.Set(sample.Value)
		}
	}
}

var syntheticBuckets = []float64{
	5, 10, 50, 100, 250, 500, 750, 1000, 2000, 5000, 10000, 20000, 30000,
}
var defaultBuckets = []float64{0}

func (a *PrometheusAdapter) handleRate(sample *metrics.Sample) {
	buckets := defaultBuckets
	if sample.Metric.Name == "coolname" {
		buckets = syntheticBuckets
	}

	if histogram := a.getHistogram(sample.Metric.Name, "k6 rate", buckets, sample.Tags); histogram != nil {
		labelValues := a.tagsToLabelValues(histogram.labelNames, sample.Tags)
		metric, err := histogram.histogramVec.GetMetricWithLabelValues(labelValues...)

		if err != nil {
			a.logger.Error(err)
		} else {
			metric.Observe(sample.Value)
		}
	}
}

func (a *PrometheusAdapter) handleTrend(sample *metrics.Sample) {
	if summary := a.getSummary(sample.Metric.Name, "k6 trend", sample.Tags); summary != nil {
		labelValues := a.tagsToLabelValues(summary.labelNames, sample.Tags)

		metric, err := summary.summaryVec.GetMetricWithLabelValues(labelValues...)
		if err != nil {
			a.logger.Error(err)
		} else {
			metric.Observe(sample.Value)
		}
	}

	if gauge := a.getGauge(sample.Metric.Name+"_current", "k6 trend (current)", sample.Tags); gauge != nil {
		labelValues := a.tagsToLabelValues(gauge.labelNames, sample.Tags)

		metric, err := gauge.gaugeVec.GetMetricWithLabelValues(labelValues...)
		if err != nil {
			a.logger.Error(err)
		} else {
			metric.Set(sample.Value)
		}
	}
}

func (a *PrometheusAdapter) getCounter(name string, helpSuffix string, tags *metrics.TagSet) *counterWithLabels { // nolint:dupl
	var counter *counterWithLabels

	if col, ok := a.metrics[name]; ok {
		if c, tok := col.(*counterWithLabels); tok {
			counter = c
		} else {
			a.logger.Warn("Wrong metric type found")
		}
	}

	if counter == nil {
		labelNames := a.tagsToLabelNames(tags)
		counter = &counterWithLabels{
			counterVec: prometheus.NewCounterVec(prometheus.CounterOpts{ // nolint:exhaustruct
				Namespace: a.Namespace,
				Subsystem: a.Subsystem,
				Name:      name,
				Help:      helpFor(name, helpSuffix),
			}, labelNames),
			labelNames: labelNames,
		}

		if err := a.registry.Register(counter.counterVec); err != nil {
			a.logger.Error(err)

			return nil
		}

		a.metrics[name] = counter
	}

	return counter
}

func (a *PrometheusAdapter) getGauge(name string, helpSuffix string, tags *metrics.TagSet) *gaugeWithLabels { // nolint:dupl
	var gauge *gaugeWithLabels

	if gau, ok := a.metrics[name]; ok {
		if g, tok := gau.(*gaugeWithLabels); tok {
			gauge = g
		} else {
			a.logger.Warn("Wrong metric type found")
		}
	}

	if gauge == nil {
		labelNames := a.tagsToLabelNames(tags)
		gauge = &gaugeWithLabels{
			gaugeVec: prometheus.NewGaugeVec(prometheus.GaugeOpts{ // nolint:exhaustruct
				Namespace: a.Namespace,
				Subsystem: a.Subsystem,
				Name:      name,
				Help:      helpFor(name, helpSuffix),
			}, labelNames),
			labelNames: labelNames,
		}

		if err := a.registry.Register(gauge.gaugeVec); err != nil {
			a.logger.Error(err)

			return nil
		}

		a.metrics[name] = gauge
	}

	return gauge
}

func (a *PrometheusAdapter) getSummary(name string, helpSuffix string, tags *metrics.TagSet) *summaryWithLabels {
	var summary *summaryWithLabels

	if sum, ok := a.metrics[name]; ok {
		if s, tok := sum.(*summaryWithLabels); tok {
			summary = s
		} else {
			a.logger.Warn("Wrong metric type found")
		}
	}

	if summary == nil {
		labelNames := a.tagsToLabelNames(tags)
		summary = &summaryWithLabels{
			summaryVec: prometheus.NewSummaryVec(prometheus.SummaryOpts{ // nolint:exhaustruct
				Namespace:  a.Namespace,
				Subsystem:  a.Subsystem,
				Name:       name,
				Help:       helpFor(name, helpSuffix),
				Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.95: 0.001, 1: 0}, // nolint:gomnd
			}, labelNames),
			labelNames: labelNames,
		}

		if err := a.registry.Register(summary.summaryVec); err != nil {
			a.logger.Error(err)

			return nil
		}

		a.metrics[name] = summary
	}

	return summary
}

func (a *PrometheusAdapter) getHistogram(name string, helpSuffix string, buckets []float64, tags *metrics.TagSet) *histogramWithLabels {
	var histogram *histogramWithLabels

	if his, ok := a.metrics[name]; ok {
		if h, tok := his.(*histogramWithLabels); tok {
			histogram = h
		} else {
			a.logger.Warn("Wrong metric type found")
		}
	}

	if histogram == nil {
		labelNames := a.tagsToLabelNames(tags)
		histogram = &histogramWithLabels{
			histogramVec: prometheus.NewHistogramVec(prometheus.HistogramOpts{ // nolint:exhaustruct
				Namespace: a.Namespace,
				Subsystem: a.Subsystem,
				Name:      name,
				Help:      helpFor(name, helpSuffix),
				Buckets:   buckets,
			}, labelNames),
			labelNames: labelNames,
		}

		if err := a.registry.Register(histogram.histogramVec); err != nil {
			a.logger.Error(err)

			return nil
		}

		a.metrics[name] = histogram
	}

	return histogram
}

func helpFor(name string, helpSuffix string) string {
	if h, ok := builtinMetrics[name]; ok {
		return h
	}

	if h, ok := builtinMetrics[strings.TrimSuffix(name, "_current")]; ok {
		return h + " (current)"
	}

	return name + " " + helpSuffix
}

var builtinMetrics = map[string]string{
	"vus":                "Current number of active virtual users",
	"vus_max":            "Max possible number of virtual users",
	"iterations":         "The aggregate number of times the VUs in the test have executed",
	"iteration_duration": "The time it took to complete one full iteration",
	"dropped_iterations": "The number of iterations that could not be started",
	"data_received":      "The amount of received data",
	"data_sent":          "The amount of data sent",
	"checks":             "The rate of successful checks",

	"http_reqs":                "How many HTTP requests has k6 generated, in total",
	"http_req_blocked":         "Time spent blocked  before initiating the request",
	"http_req_connecting":      "Time spent establishing TCP connection",
	"http_req_tls_handshaking": "Time spent handshaking TLS session",
	"http_req_sending":         "Time spent sending data",
	"http_req_waiting":         "Time spent waiting for response",
	"http_req_receiving":       "Time spent receiving response data",
	"http_req_duration":        "Total time for the request",
	"http_req_failed":          "The rate of failed requests",
}

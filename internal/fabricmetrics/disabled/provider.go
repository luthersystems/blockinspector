/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package disabled

// Copied from github.com/hyperledger/fabric@v2.1.1 common/metrics/disabled/provider.go

import (
	"github.com/luthersystems/blockinspector/internal/fabricmetrics"
)

type Provider struct{}

func (p *Provider) NewCounter(o fabricmetrics.CounterOpts) fabricmetrics.Counter { return &Counter{} }
func (p *Provider) NewGauge(o fabricmetrics.GaugeOpts) fabricmetrics.Gauge       { return &Gauge{} }
func (p *Provider) NewHistogram(o fabricmetrics.HistogramOpts) fabricmetrics.Histogram {
	return &Histogram{}
}

type Counter struct{}

func (c *Counter) Add(delta float64) {}
func (c *Counter) With(labelValues ...string) fabricmetrics.Counter {
	return c
}

type Gauge struct{}

func (g *Gauge) Add(delta float64) {}
func (g *Gauge) Set(delta float64) {}
func (g *Gauge) With(labelValues ...string) fabricmetrics.Gauge {
	return g
}

type Histogram struct{}

func (h *Histogram) Observe(value float64) {}
func (h *Histogram) With(labelValues ...string) fabricmetrics.Histogram {
	return h
}

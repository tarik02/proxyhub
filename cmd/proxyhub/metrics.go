package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	proxyConnsTotalMetric = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxyhub_proxy_conns_total",
		Help: "The total number of processed connections",
	}, []string{"proxyId"})
	proxyRecvTotalMetric = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxyhub_proxy_recv_total",
		Help: "The total number of received bytes over this proxy",
	}, []string{"proxyId"})
	proxySentTotalMetric = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxyhub_proxy_sent_total",
		Help: "The total number of sent bytes over this proxy",
	}, []string{"proxyId"})
)

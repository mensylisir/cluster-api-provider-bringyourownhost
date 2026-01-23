// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

var (
	// AgentInfoMetric exports agent version information
	AgentInfoMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "byoh_agent_info",
			Help: "Information about the BYOH agent",
		},
		[]string{"version", "os", "arch"},
	)

	// HeartbeatMetric exports the last heartbeat timestamp
	HeartbeatMetric = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "byoh_agent_last_heartbeat_timestamp",
			Help: "Timestamp of the last successful heartbeat",
		},
	)
)

func init() {
	// Register metrics with Prometheus's default registry.
	prometheus.MustRegister(AgentInfoMetric)
	prometheus.MustRegister(HeartbeatMetric)
}

// StartMetricsServer starts a Prometheus metrics server on the given address
func StartMetricsServer(addr string) {
	http.Handle("/metrics", promhttp.Handler())
	klog.Infof("Starting metrics server on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		klog.Errorf("Failed to start metrics server: %v", err)
	}
}

// UpdateHeartbeat updates the heartbeat metric to current time
func UpdateHeartbeat() {
	HeartbeatMetric.Set(float64(time.Now().Unix()))
}

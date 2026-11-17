// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package triage

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func a(svc, name string, minute int, muted bool) Alert {
	return Alert{Name: name, Service: svc, Severity: "critical", StartsAt: t0.Add(time.Duration(minute) * time.Minute), Muted: muted}
}

func summary(incs []Incident) string {
	var parts []string
	for _, i := range incs {
		parts = append(parts, fmt.Sprintf("%v<-%v", i.Roots, i.Affected))
	}
	return strings.Join(parts, " ")
}

func TestMutingEdges(t *testing.T) {
	got := graph().MutingEdges()
	want := map[string][]string{
		"frontend":        {"frontend-proxy"},
		"cart":            {"checkout", "frontend"},
		"checkout":        {"frontend"},
		"product-catalog": {"frontend"},
		"payment":         {"checkout"},
		"orders":          {"checkout"},
		"valkey-cart":     {"cart"},
		// loop-a <-> loop-b is a cycle: no muting either way.
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MutingEdges =\n%v\nwant\n%v", got, want)
	}
}

func TestIncidentsChain(t *testing.T) {
	alerts := []Alert{
		a("payment", rules.AlertRequestsStopped, 3, false),
		a("checkout", rules.AlertErrorRate, 4, true),
		a("frontend", rules.AlertErrorRate, 5, true),
		a("frontend-proxy", rules.AlertLatency, 5, true),
		a("checkout", rules.AlertMemoryNearLimit, 1, false), // not a symptom: stays with checkout
	}
	incs := graph().Incidents("shop", alerts)
	if len(incs) != 1 {
		t.Fatalf("incidents: %s", summary(incs))
	}
	inc := incs[0]
	if inc.ID != "shop:payment" || !reflect.DeepEqual(inc.Roots, []string{"payment"}) ||
		!reflect.DeepEqual(inc.Affected, []string{"checkout", "frontend", "frontend-proxy"}) {
		t.Errorf("incident = %+v", inc)
	}
	if len(inc.RootAlerts) != 1 || len(inc.AffectedAlerts) != 4 || !inc.StartsAt.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("alerts: root %d, affected %d, start %s", len(inc.RootAlerts), len(inc.AffectedAlerts), inc.StartsAt)
	}
	if IncidentOf(incs, "frontend") == nil || IncidentOf(incs, "ad") != nil {
		t.Error("IncidentOf")
	}
}

func TestIncidentsChainNeedsDirectLinks(t *testing.T) {
	// checkout is healthy: frontend is not muted by payment (Alertmanager
	// mutes only direct callers), so it is its own incident.
	incs := graph().Incidents("shop", []Alert{
		a("payment", rules.AlertErrorRate, 1, false),
		a("frontend", rules.AlertErrorRate, 2, false),
	})
	if got := summary(incs); got != "[payment]<-[] [frontend]<-[]" {
		t.Errorf("incidents: %s", got)
	}
}

func TestIncidentsTwoIndependentRoots(t *testing.T) {
	incs := graph().Incidents("shop", []Alert{
		a("orders", rules.AlertErrorRate, 2, false),
		a("valkey-cart", rules.AlertRequestsStopped, 1, false),
		a("cart", rules.AlertErrorRate, 2, true),
		a("product-catalog", rules.AlertOOMKilled, 0, false), // resource alert only: own incident
	})
	if got := summary(incs); got != "[product-catalog]<-[] [valkey-cart]<-[cart] [orders]<-[]" {
		t.Errorf("incidents: %s", got)
	}
}

func TestIncidentsSharedAffectedMergesRoots(t *testing.T) {
	incs := graph().Incidents("shop", []Alert{
		a("payment", rules.AlertErrorRate, 2, false),
		a("orders", rules.AlertLatency, 1, false),
		a("checkout", rules.AlertErrorRate, 3, true),
	})
	if len(incs) != 1 || !reflect.DeepEqual(incs[0].Roots, []string{"orders", "payment"}) || incs[0].ID != "shop:orders+payment" {
		t.Errorf("incidents: %s", summary(incs))
	}
}

func TestIncidentsCycleTerminates(t *testing.T) {
	g := NewGraph(&v1alpha1.SystemDescriptionSpec{Services: []v1alpha1.Service{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"c"}},
		{Name: "c", DependsOn: []string{"a", "d"}},
		{Name: "d"},
		{Name: "e", DependsOn: []string{"a"}},
	}})
	done := make(chan []Incident, 1)
	go func() {
		done <- g.Incidents("s", []Alert{
			a("a", rules.AlertErrorRate, 1, false),
			a("b", rules.AlertErrorRate, 1, false),
			a("c", rules.AlertErrorRate, 1, false),
			a("d", rules.AlertErrorRate, 0, false),
			a("e", rules.AlertErrorRate, 2, false),
		})
	}()
	select {
	case incs := <-done:
		// c -> d is not in the cycle: c is muted by d; a and b only by
		// cycle links (none), so they are roots; e is muted by a.
		if got := summary(incs); got != "[d]<-[c] [a]<-[e] [b]<-[]" {
			t.Errorf("incidents: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Incidents does not terminate on a cycle")
	}
}

func TestIncidentsUnknownService(t *testing.T) {
	incs := graph().Incidents("shop", []Alert{a("ghost", rules.AlertErrorRate, 0, false)})
	if got := summary(incs); got != "[ghost]<-[]" {
		t.Errorf("incidents: %s", got)
	}
	if incs := graph().Incidents("shop", nil); len(incs) != 0 {
		t.Errorf("no alerts: %v", incs)
	}
}

func TestIncidentsSourcesAndTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		alerts []Alert
		want   string
	}{
		{"no ready pods of a dependency explains the caller's SLO burn", []Alert{
			a("payment", rules.AlertNoReadyPods, 0, false),
			a("checkout", rules.AlertSLOBurnFast, 1, true),
			// checkout's SLO alert is no source, so frontend's is not muted (as in Alertmanager).
			a("frontend", rules.AlertSLOBurnSlow, 2, false),
		}, "[payment]<-[checkout] [frontend]<-[]"},
		{"an SLO burn explains nothing: both stay roots", []Alert{
			a("payment", rules.AlertSLOBurnFast, 0, false),
			a("checkout", rules.AlertSLOBurnFast, 1, false),
		}, "[payment]<-[] [checkout]<-[]"},
		{"a caller without ready pods has a cause of its own", []Alert{
			a("payment", rules.AlertRequestsStopped, 0, false),
			a("checkout", rules.AlertNoReadyPods, 1, false),
			a("checkout", rules.AlertErrorRate, 1, true),
		}, "[payment]<-[] [checkout]<-[]"},
		{"a service without data neither explains nor is explained", []Alert{
			a("payment", rules.AlertServiceTelemetryMissing, 0, false),
			a("checkout", rules.AlertErrorRate, 1, false),
		}, "[payment]<-[] [checkout]<-[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summary(graph().Incidents("shop", tc.alerts)); got != tc.want {
				t.Errorf("incidents %s, want %s", got, tc.want)
			}
		})
	}
}

func TestIncidentsSystemWide(t *testing.T) {
	sys := a("", rules.AlertTelemetryMissing, 2, false)
	alerts := []Alert{
		sys,
		a("payment", rules.AlertServiceTelemetryMissing, 0, true),
		a("cart", rules.AlertServiceTelemetryMissing, 1, true),
		a("cart", rules.AlertMemoryNearLimit, 1, false),
	}
	incs := graph().Incidents("shop", alerts)
	if got := summary(incs); got != "[cart]<-[] []<-[cart payment]" {
		t.Fatalf("incidents %s", got)
	}
	wide := IncidentOf(incs, "")
	if wide == nil || wide.ID != "shop:_system" || len(wide.RootAlerts) != 1 || wide.RootAlerts[0].Name != rules.AlertTelemetryMissing ||
		!wide.StartsAt.Equal(sys.StartsAt) || len(wide.AffectedAlerts) != 2 || wide.Roots == nil {
		t.Errorf("system incident = %+v", wide)
	}
	if inc := IncidentOf(incs, "cart"); inc == nil || inc.ID != "shop:cart" || len(inc.RootAlerts) != 1 {
		t.Errorf("cart keeps its other alert in its own incident: %+v", inc)
	}
	// Without the system alert the per-service ones are roots.
	if got := summary(graph().Incidents("shop", alerts[1:])); got != "[payment]<-[] [cart]<-[]" {
		t.Errorf("incidents %s", got)
	}
	if IncidentOf(graph().Incidents("shop", alerts[1:]), "") != nil {
		t.Error("a system incident without a system alert")
	}
}

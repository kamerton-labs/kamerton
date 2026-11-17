// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

type TunableFormat string

const (
	// FormatShare is a share from 0 to 1 (0.05 = 5%).
	FormatShare    TunableFormat = "share"
	FormatNumber   TunableFormat = "number"
	FormatInteger  TunableFormat = "integer"
	FormatQuantity TunableFormat = "quantity"
	FormatDuration TunableFormat = "duration"
)

const (
	FieldThreshold = "threshold"
	FieldFor       = "for"
)

// Tunable is one description field that decides when a rule fires. Current
// and the values given to Apply are written exactly as the field takes them.
type Tunable struct {
	Field string
	// Path is relative to the service, for example "errorRate.threshold" or
	// "operations[PlaceOrder].latency.thresholdMs".
	Path    string
	Unit    string
	Format  TunableFormat
	Current string
	// Lower: the rule gets quieter with a lower value (custom "<" conditions).
	Lower bool
	set   func(svc *v1alpha1.Service, value string) error
}

// Apply writes value into the service of sd; sd is changed in place.
func (t Tunable) Apply(sd *v1alpha1.SystemDescription, service, value string) error {
	for i := range sd.Spec.Services {
		if sd.Spec.Services[i].Name == service {
			return t.set(&sd.Spec.Services[i], value)
		}
	}
	return fmt.Errorf("no service %q", service)
}

func float(s string) (*float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Tunables lists the fields that make the rule quieter. Fields the rule
// library caps (the "for" of the aggregated per-operation latency rule) are
// left out: changing them would not change the rule.
func Tunables(sd *v1alpha1.SystemDescription, service, alert, operation string) []Tunable {
	var svc *v1alpha1.Service
	for i := range sd.Spec.Services {
		if sd.Spec.Services[i].Name == service {
			svc = &sd.Spec.Services[i]
		}
	}
	if svc == nil {
		return nil
	}
	d := sd.Spec.Defaults
	if d == nil {
		d = &v1alpha1.Defaults{}
	}
	var op *v1alpha1.Operation
	if operation != "" {
		for i := range svc.Operations {
			if svc.Operations[i].Name == operation {
				op = &svc.Operations[i]
			}
		}
	}
	errorPolicy := func(s *v1alpha1.Service) (*v1alpha1.ErrorRatePolicy, error) {
		if op == nil {
			if s.ErrorRate == nil {
				s.ErrorRate = &v1alpha1.ErrorRatePolicy{}
			}
			return s.ErrorRate, nil
		}
		for i := range s.Operations {
			if s.Operations[i].Name == operation && s.Operations[i].ErrorRate != nil {
				return s.Operations[i].ErrorRate, nil
			}
		}
		return nil, errors.New("no error rate policy of the operation")
	}
	latencyPolicy := func(s *v1alpha1.Service) (*v1alpha1.LatencyPolicy, error) {
		if op == nil {
			if s.Latency == nil {
				s.Latency = &v1alpha1.LatencyPolicy{}
			}
			return s.Latency, nil
		}
		for i := range s.Operations {
			if s.Operations[i].Name == operation && s.Operations[i].Latency != nil {
				return s.Operations[i].Latency, nil
			}
		}
		return nil, errors.New("no latency policy of the operation")
	}
	saturation := func(s *v1alpha1.Service) *v1alpha1.SaturationPolicy {
		if s.Saturation == nil {
			s.Saturation = &v1alpha1.SaturationPolicy{}
		}
		return s.Saturation
	}
	prefix := ""
	if op != nil {
		prefix = "operations[" + operation + "]."
	}
	forTunable := func(path, current string, set func(s *v1alpha1.Service, d v1alpha1.Duration) error) Tunable {
		return Tunable{Field: FieldFor, Path: path, Format: FormatDuration, Current: current,
			set: func(s *v1alpha1.Service, v string) error {
				if _, err := rules.ParseDuration(v, 0); err != nil {
					return err
				}
				return set(s, v1alpha1.Duration(v))
			}}
	}

	switch {
	case alert == rules.AlertErrorRate && operation == "",
		alert == rules.AlertOperationErrorRate && (operation == "" || op != nil && op.ErrorRate != nil):
		var ep errorRate
		if op != nil {
			ep = mergeErrorRate(d.ErrorRate, svc.ErrorRate, op.ErrorRate)
		} else {
			ep = mergeErrorRate(d.ErrorRate, svc.ErrorRate)
		}
		return []Tunable{
			{Field: FieldThreshold, Path: prefix + "errorRate.threshold", Format: FormatShare, Current: rules.FormatFloat(ep.threshold),
				set: func(s *v1alpha1.Service, v string) error {
					p, err := errorPolicy(s)
					if err != nil {
						return err
					}
					p.Threshold, err = float(v)
					return err
				}},
			forTunable(prefix+"errorRate.for", rules.FormatDuration(ep.forD), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
				p, err := errorPolicy(s)
				if err == nil {
					p.For = v
				}
				return err
			}),
		}
	case alert == rules.AlertLatency && operation == "",
		alert == rules.AlertOperationLatency && (operation == "" || op != nil && op.Latency != nil):
		var lp latency
		if op != nil {
			lp = mergeLatency(d.Latency, svc.Latency, op.Latency)
		} else {
			lp = mergeLatency(d.Latency, svc.Latency)
		}
		out := []Tunable{{Field: FieldThreshold, Path: prefix + "latency.thresholdMs", Unit: "ms", Format: FormatNumber, Current: rules.FormatFloat(lp.thresholdMs),
			set: func(s *v1alpha1.Service, v string) error {
				p, err := latencyPolicy(s)
				if err != nil {
					return err
				}
				p.ThresholdMs, err = float(v)
				return err
			}}}
		if alert == rules.AlertOperationLatency && op == nil {
			return out
		}
		return append(out, forTunable(prefix+"latency.for", rules.FormatDuration(lp.forD), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
			p, err := latencyPolicy(s)
			if err == nil {
				p.For = v
			}
			return err
		}))
	case alert == rules.AlertLatencyAboveNormal:
		lp := mergeLatency(d.Latency, svc.Latency)
		return []Tunable{{Field: FieldThreshold, Path: "latency.aboveNormalFactor", Unit: "×", Format: FormatNumber, Current: rules.FormatFloat(lp.factor),
			set: func(s *v1alpha1.Service, v string) error {
				p, err := latencyPolicy(s)
				if err != nil {
					return err
				}
				p.AboveNormalFactor, err = float(v)
				return err
			}}}
	case alert == rules.AlertRequestsStopped:
		tp := mergeTraffic(svc, d.TrafficDrop, svc.TrafficDrop)
		return []Tunable{forTunable("trafficDrop.for", rules.FormatDuration(tp.forD), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
			if s.TrafficDrop == nil {
				s.TrafficDrop = &v1alpha1.TrafficDropPolicy{}
			}
			s.TrafficDrop.For = v
			return nil
		})}
	case alert == rules.AlertNoReadyPods:
		np := mergeNoReadyPods(d.NoReadyPods, svc.NoReadyPods)
		return []Tunable{forTunable("noReadyPods.for", rules.FormatDuration(np.forD), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
			if s.NoReadyPods == nil {
				s.NoReadyPods = &v1alpha1.NoReadyPodsPolicy{}
			}
			s.NoReadyPods.For = v
			return nil
		})}
	case alert == rules.AlertQueueLag && svc.Queue != nil:
		return []Tunable{
			{Field: FieldThreshold, Path: "queue.maxLag", Unit: "messages", Format: FormatNumber, Current: rules.FormatFloat(svc.Queue.MaxLag),
				set: func(s *v1alpha1.Service, v string) error {
					if s.Queue == nil {
						return errors.New("no queue")
					}
					f, err := float(v)
					if err == nil {
						s.Queue.MaxLag = *f
					}
					return err
				}},
			forTunable("queue.for", rules.FormatDuration(mustDuration(string(svc.Queue.For), builtinQueueFor)), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
				if s.Queue == nil {
					return errors.New("no queue")
				}
				s.Queue.For = v
				return nil
			}),
		}
	case alert == rules.AlertRestarts:
		sp := mergeSaturation(d.Saturation, svc.Saturation)
		return []Tunable{{Field: FieldThreshold, Path: "saturation.maxRestartsPerHour", Unit: "per hour", Format: FormatInteger, Current: strconv.Itoa(sp.maxRestarts),
			set: func(s *v1alpha1.Service, v string) error {
				n, err := strconv.ParseInt(v, 10, 32)
				if err == nil {
					n32 := int32(n)
					saturation(s).MaxRestartsPerHour = &n32
				}
				return err
			}}}
	}

	saturationFor := func() Tunable {
		sp := mergeSaturation(d.Saturation, svc.Saturation)
		return forTunable("saturation.for", rules.FormatDuration(sp.forD), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
			saturation(s).For = v
			return nil
		})
	}
	floatField := func(path, unit string, format TunableFormat, current float64, field func(*v1alpha1.SaturationPolicy) **float64) Tunable {
		return Tunable{Field: FieldThreshold, Path: path, Unit: unit, Format: format, Current: rules.FormatFloat(current),
			set: func(s *v1alpha1.Service, v string) error {
				f, err := float(v)
				if err == nil {
					*field(saturation(s)) = f
				}
				return err
			}}
	}
	sp := mergeSaturation(d.Saturation, svc.Saturation)
	switch alert {
	case rules.AlertMemory:
		current := ""
		for _, l := range []*v1alpha1.SaturationPolicy{d.Saturation, svc.Saturation} {
			if l != nil && l.Memory != "" {
				current = l.Memory
			}
		}
		return []Tunable{{Field: FieldThreshold, Path: "saturation.memory", Format: FormatQuantity, Current: current,
			set: func(s *v1alpha1.Service, v string) error {
				if _, err := resource.ParseQuantity(v); err != nil {
					return err
				}
				saturation(s).Memory = v
				return nil
			}}, saturationFor()}
	case rules.AlertMemoryNearLimit:
		return []Tunable{floatField("saturation.memoryLimitShare", "", FormatShare, sp.memoryLimitShare,
			func(p *v1alpha1.SaturationPolicy) **float64 { return &p.MemoryLimitShare }), saturationFor()}
	case rules.AlertCPU:
		return []Tunable{floatField("saturation.cpuCores", "cores", FormatNumber, sp.cpuCores,
			func(p *v1alpha1.SaturationPolicy) **float64 { return &p.CPUCores }), saturationFor()}
	case rules.AlertCPUNearLimit:
		return []Tunable{floatField("saturation.cpuLimitShare", "", FormatShare, sp.cpuLimitShare,
			func(p *v1alpha1.SaturationPolicy) **float64 { return &p.CPULimitShare }), saturationFor()}
	case rules.AlertCPUAboveNormal:
		return []Tunable{floatField("saturation.cpuAboveNormalFactor", "×", FormatNumber, sp.cpuFactor,
			func(p *v1alpha1.SaturationPolicy) **float64 { return &p.CPUAboveNormalFactor }), saturationFor()}
	}

	for _, cr := range svc.CustomRules {
		if rules.CustomAlertName(cr.Name) != alert {
			continue
		}
		name := cr.Name
		custom := func(s *v1alpha1.Service) (*v1alpha1.CustomRule, error) {
			for i := range s.CustomRules {
				if s.CustomRules[i].Name == name {
					return &s.CustomRules[i], nil
				}
			}
			return nil, errors.New("no custom rule " + name)
		}
		path := "customRules[" + name + "]."
		var out []Tunable
		if c := cr.Condition; c != nil && cr.Expr == "" && c.Operator != "eq" && c.Operator != "ne" {
			out = append(out, Tunable{Field: FieldThreshold, Path: path + "condition.threshold", Unit: cr.Unit, Format: FormatNumber,
				Current: rules.FormatFloat(c.Threshold), Lower: c.Operator == "lt" || c.Operator == "lte",
				set: func(s *v1alpha1.Service, v string) error {
					r, err := custom(s)
					if err != nil {
						return err
					}
					if r.Condition == nil {
						return errors.New("no condition")
					}
					f, err := float(v)
					if err == nil {
						r.Condition.Threshold = *f
					}
					return err
				}})
		}
		return append(out, forTunable(path+"for", rules.FormatDuration(mustDuration(string(cr.For), builtinCustomFor)), func(s *v1alpha1.Service, v v1alpha1.Duration) error {
			r, err := custom(s)
			if err == nil {
				r.For = v
			}
			return err
		}))
	}
	return nil
}

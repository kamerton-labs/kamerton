// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package stack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	KindPrometheus    = "prometheus"
	KindAlertmanager  = "alertmanager"
	KindGrafana       = "grafana"
	KindLoki          = "loki"
	KindTempo         = "tempo"
	KindOTelCollector = "otel-collector"
)

// Kinds is in the order the UI shows them.
var Kinds = []string{KindPrometheus, KindAlertmanager, KindGrafana, KindLoki, KindTempo, KindOTelCollector}

var Titles = map[string]string{
	KindPrometheus: "Prometheus", KindAlertmanager: "Alertmanager", KindGrafana: "Grafana",
	KindLoki: "Loki", KindTempo: "Tempo", KindOTelCollector: "OpenTelemetry Collector",
}

func ValidKind(k string) bool {
	_, ok := Titles[k]
	return ok
}

// The built-in set's addresses come from the flags and may be overridden one by
// one in the UI.
const (
	BuiltinID   = "main"
	BuiltinName = "Main"
)

const (
	// OriginBundled: the address equals the flag value of a program installed with
	// Kamerton. OriginOwn is any other address.
	OriginBundled = "bundled"
	OriginOwn     = "own"
	OriginNone    = "none"
)

// Set is stored as is. Nothing in it is secret: addresses with credentials are
// rejected.
type Set struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// URLs maps a kind to an address. For the built-in set it holds only
	// overrides of the flag values; an empty value disconnects the program.
	URLs map[string]string `json:"urls,omitempty"`
	// RuleLabels go on the PrometheusRule objects of systems using the set, so the
	// right Prometheus picks them up.
	RuleLabels               map[string]string `json:"ruleLabels,omitempty"`
	AlertmanagerConfigLabels map[string]string `json:"alertmanagerConfigLabels,omitempty"`
	CreatedAt                time.Time         `json:"createdAt,omitzero"`
	UpdatedAt                time.Time         `json:"updatedAt,omitzero"`
}

// State is keyed by set ID. The built-in set is present only when something of
// it was changed.
type State struct {
	Sets map[string]*Set `json:"sets,omitempty"`
}

type Flags struct {
	URLs map[string]string
	// External marks bring-your-own programs: not installed with Kamerton.
	External map[string]bool
}

type Program struct {
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	Origin string `json:"origin"`
	// FlagURL is the install-time address (built-in set only).
	FlagURL    string `json:"flagURL,omitempty"`
	Overridden bool   `json:"overridden,omitempty"`
}

type Resolved struct {
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	Builtin                  bool              `json:"builtin"`
	Programs                 []Program         `json:"programs"`
	RuleLabels               map[string]string `json:"ruleLabels"`
	AlertmanagerConfigLabels map[string]string `json:"alertmanagerConfigLabels"`
}

func (r *Resolved) URL(kind string) string {
	for _, p := range r.Programs {
		if p.Kind == kind {
			return p.URL
		}
	}
	return ""
}

var (
	ErrUnknownSet = errors.New("no such set of programs")
	ErrBuiltin    = errors.New("the built-in set cannot be deleted")
)

// InputError messages are shown to the person as they are.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

type InUseError struct{ Systems []string }

func (e *InUseError) Error() string {
	return "the set is used by systems: " + strings.Join(e.Systems, ", ")
}

type Sets struct {
	Store StateStore
	Flags Flags
	// Namespace is Kamerton's namespace: a short service name in an address
	// (http://svc:port) means a service there.
	Namespace string
	Now       func() time.Time
}

func (s *Sets) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Sets) FlagURL(kind string) string { return s.Flags.URLs[kind] }

func (s *Sets) origin(kind, u string) string {
	switch {
	case u == "":
		return OriginNone
	case !s.Flags.External[kind] && s.Flags.URLs[kind] != "" && SameAddress(u, s.Flags.URLs[kind], s.Namespace):
		return OriginBundled
	default:
		return OriginOwn
	}
}

func (s *Sets) resolve(st *Set, builtin bool) Resolved {
	r := Resolved{ID: st.ID, Name: st.Name, Builtin: builtin, RuleLabels: copyMap(st.RuleLabels), AlertmanagerConfigLabels: copyMap(st.AlertmanagerConfigLabels)}
	if builtin && r.Name == "" {
		r.Name = BuiltinName
	}
	for _, k := range Kinds {
		p := Program{Kind: k}
		if builtin {
			p.FlagURL = s.Flags.URLs[k]
			if v, ok := st.URLs[k]; ok {
				p.URL, p.Overridden = v, v != p.FlagURL
			} else {
				p.URL = p.FlagURL
			}
		} else {
			p.URL = st.URLs[k]
		}
		p.Origin = s.origin(k, p.URL)
		r.Programs = append(r.Programs, p)
	}
	return r
}

func (s *Sets) state(ctx context.Context) (*State, error) {
	if s.Store == nil {
		return &State{Sets: map[string]*Set{}}, nil
	}
	return s.Store.Get(ctx)
}

// List returns the built-in set first, then the others by name.
func (s *Sets) List(ctx context.Context) ([]Resolved, error) {
	st, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	return s.list(st), nil
}

func (s *Sets) list(st *State) []Resolved {
	b := st.Sets[BuiltinID]
	if b == nil {
		b = &Set{ID: BuiltinID}
	}
	out := []Resolved{s.resolve(b, true)}
	var others []Resolved
	for id, set := range st.Sets {
		if id != BuiltinID {
			others = append(others, s.resolve(set, false))
		}
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].Name != others[j].Name {
			return others[i].Name < others[j].Name
		}
		return others[i].ID < others[j].ID
	})
	return append(out, others...)
}

func (s *Sets) Get(ctx context.Context, id string) (*Resolved, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, ErrUnknownSet
}

// Resolve returns the set a system uses: "" is the built-in set, and an
// unknown ID falls back to it with unknown = true.
func (s *Sets) Resolve(ctx context.Context, id string) (r Resolved, unknown bool, err error) {
	if id == "" {
		id = BuiltinID
	}
	got, err := s.Get(ctx, id)
	switch {
	case errors.Is(err, ErrUnknownSet):
		b, err := s.Get(ctx, BuiltinID)
		if err != nil {
			return Resolved{}, true, err
		}
		return *b, true, nil
	case err != nil:
		return Resolved{}, false, err
	}
	return *got, false, nil
}

// Builtin ignores stored overrides: it is the fallback when the store cannot be
// read.
func (s *Sets) Builtin() Resolved { return s.resolve(&Set{ID: BuiltinID}, true) }

// Input changes a set. Nil fields stay as they are.
type Input struct {
	// ID of a new set; derived from the name when empty. Ignored on update.
	ID   string  `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
	// URLs sets addresses by kind; kinds not listed stay as they are.
	URLs                     map[string]string  `json:"urls,omitempty"`
	RuleLabels               *map[string]string `json:"ruleLabels,omitempty"`
	AlertmanagerConfigLabels *map[string]string `json:"alertmanagerConfigLabels,omitempty"`
}

var idRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func (s *Sets) Create(ctx context.Context, in Input) (*Resolved, error) {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return nil, &InputError{"Give the set a name."}
	}
	var created string
	_, err := s.update(ctx, func(st *State) error {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			id = uniqueID(st, slug(*in.Name))
		} else if !idRe.MatchString(id) || len(id) > 63 {
			return &InputError{"Set ID: lowercase Latin letters, digits and dashes, up to 63 characters."}
		} else if st.Sets[id] != nil || id == BuiltinID {
			return &InputError{"A set with ID \"" + id + "\" already exists."}
		}
		set := &Set{ID: id, CreatedAt: s.now()}
		if err := s.apply(st, set, in); err != nil {
			return err
		}
		st.Sets[id] = set
		created = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, created)
}

func (s *Sets) Update(ctx context.Context, id string, in Input) (*Resolved, error) {
	_, err := s.update(ctx, func(st *State) error {
		set, err := s.stored(st, id)
		if err != nil {
			return err
		}
		return s.apply(st, set, in)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// ResetAddress returns a kind of the built-in set to its flag value, or
// disconnects it in another set.
func (s *Sets) ResetAddress(ctx context.Context, id, kind string) (*Resolved, error) {
	if !ValidKind(kind) {
		return nil, &InputError{"No such program: " + kind}
	}
	_, err := s.update(ctx, func(st *State) error {
		set, err := s.stored(st, id)
		if err != nil {
			return err
		}
		delete(set.URLs, kind)
		set.UpdatedAt = s.now()
		if id == BuiltinID && len(set.URLs) == 0 && set.Name == "" && len(set.RuleLabels) == 0 && len(set.AlertmanagerConfigLabels) == 0 {
			delete(st.Sets, BuiltinID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Sets) Delete(ctx context.Context, id string, usedBy []string) error {
	if id == BuiltinID {
		return ErrBuiltin
	}
	if len(usedBy) > 0 {
		return &InUseError{Systems: usedBy}
	}
	_, err := s.update(ctx, func(st *State) error {
		if st.Sets[id] == nil {
			return ErrUnknownSet
		}
		delete(st.Sets, id)
		return nil
	})
	return err
}

// stored creates the built-in set on its first change.
func (s *Sets) stored(st *State, id string) (*Set, error) {
	if set := st.Sets[id]; set != nil {
		return set, nil
	}
	if id != BuiltinID {
		return nil, ErrUnknownSet
	}
	set := &Set{ID: BuiltinID, CreatedAt: s.now()}
	st.Sets[BuiltinID] = set
	return set, nil
}

func (s *Sets) apply(st *State, set *Set, in Input) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return &InputError{"Give the set a name."}
		}
		if len([]rune(name)) > 60 {
			return &InputError{"The set name must be at most 60 characters."}
		}
		for _, o := range s.list(st) {
			if o.ID != set.ID && strings.EqualFold(o.Name, name) {
				return &InputError{"A set named \"" + name + "\" already exists: choose another name."}
			}
		}
		if set.ID == BuiltinID && name == BuiltinName {
			name = ""
		}
		set.Name = name
	}
	for kind, raw := range in.URLs {
		if !ValidKind(kind) {
			return &InputError{"No such program: " + kind}
		}
		u, err := NormalizeURL(raw)
		if err != nil {
			return err
		}
		if set.URLs == nil {
			set.URLs = map[string]string{}
		}
		if set.ID == BuiltinID && u == s.Flags.URLs[kind] {
			delete(set.URLs, kind) // back to the flag value
			continue
		}
		if set.ID != BuiltinID && u == "" {
			delete(set.URLs, kind)
			continue
		}
		set.URLs[kind] = u
	}
	if in.RuleLabels != nil {
		l, err := CheckLabels(*in.RuleLabels, "PrometheusRule")
		if err != nil {
			return err
		}
		set.RuleLabels = l
	}
	if in.AlertmanagerConfigLabels != nil {
		l, err := CheckLabels(*in.AlertmanagerConfigLabels, "AlertmanagerConfig")
		if err != nil {
			return err
		}
		set.AlertmanagerConfigLabels = l
	}
	set.UpdatedAt = s.now()
	return nil
}

func (s *Sets) update(ctx context.Context, mutate func(*State) error) (*State, error) {
	if s.Store == nil {
		return nil, errors.New("sets: no store")
	}
	return s.Store.Update(ctx, func(st *State) error {
		if st.Sets == nil {
			st.Sets = map[string]*Set{}
		}
		return mutate(st)
	})
}

func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", &InputError{"The address must start with http:// or https://, for example http://prometheus.monitoring:9090"}
	}
	if u.User != nil {
		return "", &InputError{"The address must not contain a user name and password: Kamerton stores addresses in plain text."}
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", &InputError{"Enter the address without anything after \"?\" or \"#\"."}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Labels Kamerton puts on every object itself; a set may repeat them with
// the same value (found selectors often do) but not change them.
var reservedLabels = map[string]string{
	"app.kubernetes.io/managed-by": "kamerton",
	"kamerton.dev/managed":         "true",
	"kamerton.dev/system":          "",
}

func CheckLabels(in map[string]string, what string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return nil, &InputError{fmt.Sprintf("Label %s for %s: the name is not valid in Kubernetes (%s).", k, what, errs[0])}
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return nil, &InputError{fmt.Sprintf("Label %s for %s: the value %q is not valid in Kubernetes (%s).", k, what, v, errs[0])}
		}
		if want, ok := reservedLabels[k]; ok && (want == "" || want != v) {
			return nil, &InputError{fmt.Sprintf("Kamerton sets label %s itself: it cannot be changed.", k)}
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "i",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'қ': "k", 'ғ': "g", 'ү': "u", 'ұ': "u", 'ң': "n", 'ө': "o", 'һ': "h", 'ә': "a", 'і': "i",
}

func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case translit[r] != "":
			b.WriteString(translit[r])
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 50 {
		out = strings.Trim(out[:50], "-")
	}
	if out == "" {
		out = "set"
	}
	return out
}

func uniqueID(st *State, base string) string {
	id := base
	for n := 2; st.Sets[id] != nil || id == BuiltinID; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SameAddress compares an address in a set with a found one. Inside a
// cluster one service has several spellings: svc (in Kamerton's namespace ns),
// svc.ns, svc.ns.svc, svc.ns.svc.cluster.local, with or without the default port.
func SameAddress(a, b, ns string) bool {
	norm := func(raw string) string {
		u, err := url.Parse(strings.ToLower(strings.TrimSpace(raw)))
		if err != nil || u.Host == "" {
			return strings.TrimRight(raw, "/")
		}
		host, port := u.Hostname(), u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		host = strings.TrimSuffix(strings.TrimSuffix(host, "."), ".cluster.local")
		if net.ParseIP(host) == nil {
			switch parts := strings.Split(host, "."); {
			case len(parts) == 1 && ns != "":
				host += "." + ns + ".svc"
			case len(parts) == 2:
				host += ".svc"
			}
		}
		return u.Scheme + "://" + host + ":" + port + strings.TrimRight(u.Path, "/")
	}
	return norm(a) == norm(b)
}

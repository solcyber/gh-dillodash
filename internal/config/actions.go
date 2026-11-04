package config

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// This file holds the config of the actions that replace the web page:
// [merge] methods, the maintenance window's reserved span, and the
// allowlist of workflows X may run ([[workflows.dispatch]]).

// MergeConfig is [merge]: how M merges a PR, per repo.
type MergeConfig struct {
	// Methods is the merge method per repo (merge | squash | rebase); a
	// repo not listed merges with [workflows.release_pr] merge_method, or
	// as a merge commit.
	Methods map[model.Repo]string
}

// Method is repo's merge method, or def when it is not listed.
func (m MergeConfig) Method(repo model.Repo, def string) string {
	for r, v := range m.Methods {
		if strings.EqualFold(r.String(), repo.String()) {
			return v
		}
	}
	return def
}

// ReservedWindow is the span around the maintenance window in which the
// apply dispatcher refuses an immediate apply to a reserved stack, even a
// harmless one, without the outside-window flag: dillodash offers the
// flag (with a reason) instead of posting a command that is refused.
type ReservedWindow struct {
	From, To clock.HM
	// Stacks are the reserved stacks (path.Match patterns such as
	// "prod/*"); empty means every stack.
	Stacks []string
}

// Covers reports whether now (in the window's zone) is inside the span
// and stack is reserved. A span whose to is before its from wraps
// midnight.
func (r *ReservedWindow) Covers(local *clock.Local, now time.Time, stack string) bool {
	if r == nil || local == nil {
		return false
	}
	t := local.In(now)
	min := t.Hour()*60 + t.Minute()
	from, to := r.From.H*60+r.From.M, r.To.H*60+r.To.M
	inside := from <= min && min < to
	if to <= from {
		inside = min >= from || min < to
	}
	if !inside {
		return false
	}
	if len(r.Stacks) == 0 {
		return true
	}
	for _, p := range r.Stacks {
		if ok, _ := path.Match(p, stack); ok || p == stack {
			return true
		}
	}
	return false
}

// DispatchInput is one input of a dispatchable workflow.
type DispatchInput struct {
	Name        string
	Type        string // string | boolean | choice | number
	Default     string
	Options     []string // choice
	Required    bool
	Description string
}

// DispatchConfig is one [[workflows.dispatch]] entry: a workflow file X
// may run with workflow_dispatch, or a repository_dispatch event it may
// fire. Nothing else is ever dispatched.
type DispatchConfig struct {
	Name     string // the menu label
	Repo     model.Repo
	Workflow string // workflow file (workflow_dispatch); "" when Event is set
	Ref      string // the ref the workflow runs on (default "main")
	Event    string // repository_dispatch event_type; "" when Workflow is set
	Confirm  string // an extra line shown in the confirm
	Inputs   []DispatchInput
}

// Kind is "workflow" or "event".
func (d DispatchConfig) Kind() string {
	if d.Event != "" {
		return "event"
	}
	return "workflow"
}

// InputTypes are the types a dispatch input may have.
var InputTypes = []string{"string", "boolean", "choice", "number"}

// Value converts a typed answer to the JSON value the API takes:
// workflow_dispatch inputs are strings (GitHub converts), a
// repository_dispatch payload keeps booleans and numbers typed.
func (in DispatchInput) Value(s string, typed bool) (any, error) {
	s = strings.TrimSpace(s)
	switch in.Type {
	case "boolean":
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not true or false", in.Name, s)
		}
		if typed {
			return b, nil
		}
		return strconv.FormatBool(b), nil
	case "number":
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", in.Name, s)
		}
		if typed {
			return f, nil
		}
		return s, nil
	case "choice":
		if !contains(in.Options, s) {
			return nil, fmt.Errorf("%s: %q is not one of %s", in.Name, s, strings.Join(in.Options, ", "))
		}
	}
	if in.Required && s == "" {
		return nil, fmt.Errorf("%s is required", in.Name)
	}
	return s, nil
}

func (p *parser) parseMerge(top *table, cfg *Config) {
	t, _ := top.sub("merge")
	if raw, ok := t.get("methods"); ok {
		m, isTable := raw.(map[string]any)
		if !isTable {
			t.errf("methods", "expected a table like { \"owner/name\" = \"squash\" }, got %s", typeName(raw))
		} else {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				r, ok := model.ParseRepo(k)
				if !ok {
					t.errf("methods", "%q is not \"owner/name\"", k)
					continue
				}
				v, ok := m[k].(string)
				if !ok || !contains(MergeChoices, v) {
					t.errf("methods", "%s: %v is not one of %s", k, m[k], strings.Join(MergeChoices, ", "))
					continue
				}
				if cfg.Merge.Methods == nil {
					cfg.Merge.Methods = map[model.Repo]string{}
				}
				cfg.Merge.Methods[r] = v
			}
		}
	}
	t.done()
}

// parseReserved reads the maintenance window's reserved span: from and
// to both set, or neither.
func (p *parser) parseReserved(t *table, w *MaintenanceWindowConfig) {
	from := strings.TrimSpace(t.str("reserved_from", ""))
	to := strings.TrimSpace(t.str("reserved_to", ""))
	stacks, _ := t.strs("reserved_stacks", nil)
	if from == "" && to == "" {
		if len(stacks) > 0 {
			t.warnf("reserved_stacks", "has no effect without reserved_from and reserved_to")
		}
		return
	}
	if from == "" || to == "" {
		t.errf("reserved_from, reserved_to", "set both or neither")
		return
	}
	f, err1 := clock.ParseHM(from)
	e, err2 := clock.ParseHM(to)
	if err1 != nil || err2 != nil {
		t.errf("reserved_from, reserved_to", "expected HH:MM, got %q and %q", from, to)
		return
	}
	for _, s := range stacks {
		if _, err := path.Match(s, ""); err != nil {
			t.errf("reserved_stacks", "%q is not a valid pattern", s)
			return
		}
	}
	w.Reserved = &ReservedWindow{From: f, To: e, Stacks: stacks}
}

func (p *parser) parseDispatch(wt *table, cfg *Config) {
	raw, ok := wt.get("dispatch")
	if !ok {
		return
	}
	arr, ok := raw.([]any)
	if !ok {
		wt.errf("dispatch", "expected [[workflows.dispatch]] entries, got %s", typeName(raw))
		return
	}
	names := map[string]bool{}
	for i, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			wt.errf("dispatch", "entry %d: expected a table, got %s", i+1, typeName(e))
			continue
		}
		t := p.newTable(fmt.Sprintf("[[workflows.dispatch]] #%d", i+1), m)
		d := DispatchConfig{}
		d.Name = strings.TrimSpace(t.nonEmpty("name", "?"))
		if r, ok := model.ParseRepo(strings.TrimSpace(t.str("repo", ""))); ok {
			d.Repo = r
		} else {
			t.errf("repo", "must be \"owner/name\"")
		}
		d.Workflow = strings.TrimSpace(t.str("workflow", ""))
		d.Event = strings.TrimSpace(t.str("event", ""))
		d.Ref = strings.TrimSpace(t.str("ref", "main"))
		d.Confirm = strings.TrimSpace(t.str("confirm", ""))
		if (d.Workflow == "") == (d.Event == "") {
			t.errf("workflow, event", "set exactly one: workflow (a workflow file) or event (a repository_dispatch event_type)")
		}
		if d.Event != "" {
			if _, ok := m["ref"]; ok {
				t.warnf("ref", "has no effect on a repository_dispatch event")
			}
		}
		if names[strings.ToLower(d.Name)] {
			t.errf("name", "%q is listed twice", d.Name)
		}
		names[strings.ToLower(d.Name)] = true
		if rawIn, ok := t.get("inputs"); ok {
			list, ok := rawIn.([]any)
			if !ok {
				t.errf("inputs", "expected an array of tables, got %s", typeName(rawIn))
			}
			for j, ie := range list {
				im, ok := ie.(map[string]any)
				if !ok {
					t.errf("inputs", "entry %d: expected a table, got %s", j+1, typeName(ie))
					continue
				}
				it := p.newTable(fmt.Sprintf("%s inputs #%d", t.name, j+1), im)
				in := DispatchInput{}
				in.Name = strings.TrimSpace(it.nonEmpty("name", "?"))
				in.Type = it.choice("type", "string", InputTypes)
				in.Options, _ = it.strs("options", nil)
				in.Required = it.boolean("required", false)
				in.Description = strings.TrimSpace(it.str("description", ""))
				switch v := im["default"].(type) {
				case nil:
				case string:
					in.Default = v
				case bool:
					in.Default = strconv.FormatBool(v)
				case int64:
					in.Default = strconv.FormatInt(v, 10)
				case float64:
					in.Default = strconv.FormatFloat(v, 'f', -1, 64)
				default:
					it.errf("default", "expected a string, boolean or number, got %s", typeName(v))
				}
				it.used["default"] = true
				if in.Type == "choice" && len(in.Options) == 0 {
					it.errf("options", "a choice input needs options")
				}
				if in.Default != "" {
					if _, err := in.Value(in.Default, false); err != nil {
						it.errf("default", "%s", err)
					}
				}
				it.done()
				d.Inputs = append(d.Inputs, in)
			}
		}
		t.done()
		cfg.Dispatches = append(cfg.Dispatches, d)
	}
}

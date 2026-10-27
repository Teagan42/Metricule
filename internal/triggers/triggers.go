// Package triggers provisions and removes the PostgreSQL notification objects
// Metricule owns.
//
// Management is opt-in (AD-006): every operation returns without touching the
// database unless configuration set triggers.manage. Provisioning uses a separate
// connection, never the collection pool (§9.3). Every managed object carries an
// ownership marker, so removal never touches user-created objects.
package triggers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// FunctionName is the shared trigger function created in the managed schema.
//
// One function serves every table and channel: the channel is a trigger argument,
// so a table watched by two channels does not need two functions (§9.3).
const FunctionName = "metricule_notify_change"

const (
	functionMarker = "metricule:owned:v1"
	triggerPrefix  = "metricule_notify_"
)

// Subscription is one table/channel pair that should notify on change.
type Subscription struct {
	// Table is schema-qualified, such as public.jobs.
	Table   string
	Channel string
	// Events are lowercase insert, update, or delete. Empty means all three.
	Events []string
}

// Target is one PostgreSQL source's desired managed state.
type Target struct {
	SourceID string
	// Schema holds the managed function. Created when absent.
	Schema string
	// Manage gates provisioning. Status and removal work without it.
	Manage            bool
	CleanupOnShutdown bool
	// DSN is resolved for provisioning and must never be logged.
	DSN           string
	Subscriptions []Subscription
	// Warnings are non-fatal configuration findings, reported by the CLI.
	Warnings []string
}

func (t Target) validate() error {
	if strings.TrimSpace(t.Schema) == "" {
		return fmt.Errorf("source %q: triggers.schema is required", t.SourceID)
	}
	for _, s := range t.Subscriptions {
		if _, _, err := splitTable(s.Table); err != nil {
			return fmt.Errorf("source %q: %w", t.SourceID, err)
		}
		if strings.TrimSpace(s.Channel) == "" {
			return fmt.Errorf("source %q: table %q has no notification channel", t.SourceID, s.Table)
		}
		if len([]byte(s.Channel)) > 63 {
			return fmt.Errorf("source %q: channel %q exceeds PostgreSQL's 63-byte identifier limit", t.SourceID, s.Channel)
		}
		if _, err := eventClause(s.Events); err != nil {
			return fmt.Errorf("source %q: %w", t.SourceID, err)
		}
	}
	return nil
}

// Action is one step of a plan. Statements is empty when nothing needs to change.
type Action struct {
	// Kind is create, replace, drop, or unchanged.
	Kind    string
	Object  string
	Table   string
	Channel string
	// Statements are executed in order. Empty means the object already matches.
	Statements []string
}

// Report describes what a plan would do, or what apply/remove did.
type Report struct {
	Source    string
	Schema    string
	Actions   []Action
	Warnings  []string
	Unchanged int
}

// Changed reports whether the report contains work.
func (r Report) Changed() bool {
	for _, a := range r.Actions {
		if len(a.Statements) > 0 {
			return true
		}
	}
	return false
}

// ObjectState classifies one desired object against the catalog.
type ObjectState string

// Catalog states reported by Status.
const (
	StatePresent ObjectState = "present"
	StateMissing ObjectState = "missing"
	StateChanged ObjectState = "changed"
	StateForeign ObjectState = "foreign"
	StateOrphan  ObjectState = "orphan"
)

// ObjectStatus is the state of one desired trigger.
type ObjectStatus struct {
	Table   string
	Channel string
	Trigger string
	State   ObjectState
}

// StatusReport is a read-only inventory of managed objects.
type StatusReport struct {
	Source        string
	Schema        string
	SchemaPresent bool
	Function      ObjectState
	Objects       []ObjectStatus
	Warnings      []string
}

// Manager runs trigger operations on one provisioning connection.
type Manager struct {
	conn *pgx.Conn
}

// Open connects with the provisioning DSN.
//
// Errors never quote the DSN: pgx connection errors echo host and user, and a
// caller must not risk a password reaching a log line.
func Open(ctx context.Context, dsn string) (*Manager, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("trigger management requires a provisioning DSN")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect for trigger management: %w", err)
	}
	return &Manager{conn: conn}, nil
}

// Close releases the provisioning connection.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil || m.conn == nil {
		return nil
	}
	return m.conn.Close(ctx)
}

// Plan reports the SQL that Apply would run, without changing anything.
func (m *Manager) Plan(ctx context.Context, t Target) (Report, error) {
	return buildPlan(ctx, m.conn, t)
}

// Apply converges the database to the target state in one transaction.
func (m *Manager) Apply(ctx context.Context, t Target) (Report, error) {
	if !t.Manage {
		return Report{}, fmt.Errorf("source %q: triggers.manage is false; refusing to provision", t.SourceID)
	}
	report, err := buildPlan(ctx, m.conn, t)
	if err != nil {
		return Report{}, err
	}
	if !report.Changed() {
		return report, nil
	}

	tx, err := m.conn.Begin(ctx)
	if err != nil {
		return report, fmt.Errorf("begin trigger provisioning: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, action := range report.Actions {
		for _, stmt := range action.Statements {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return report, fmt.Errorf("%s %s: %w", action.Kind, action.Object, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return report, fmt.Errorf("commit trigger provisioning: %w", err)
	}
	return report, nil
}

// Status inventories managed objects and the desired state.
func (m *Manager) Status(ctx context.Context, t Target) (StatusReport, error) {
	if err := t.validate(); err != nil {
		return StatusReport{}, err
	}
	report := StatusReport{Source: t.SourceID, Schema: t.Schema, Warnings: t.Warnings}

	present, err := schemaPresent(ctx, m.conn, t.Schema)
	if err != nil {
		return report, err
	}
	report.SchemaPresent = present

	fn, err := inspectFunction(ctx, m.conn, t.Schema)
	if err != nil {
		return report, err
	}
	switch {
	case !fn.exists:
		report.Function = StateMissing
	case fn.owned:
		report.Function = StatePresent
	default:
		report.Function = StateForeign
	}

	desired := map[string]struct{}{}
	for _, sub := range sortedSubscriptions(t.Subscriptions) {
		tableSchema, tableName, err := splitTable(sub.Table)
		if err != nil {
			return report, err
		}
		name := TriggerName(sub.Table, sub.Channel)
		desired[name] = struct{}{}

		tr, err := inspectTrigger(ctx, m.conn, tableSchema, tableName, name)
		if err != nil {
			return report, err
		}
		state := StatePresent
		switch {
		case !tr.exists:
			state = StateMissing
		case !tr.owned:
			state = StateForeign
		case tr.hash != subscriptionHash(sub):
			state = StateChanged
		}
		report.Objects = append(report.Objects, ObjectStatus{
			Table: sub.Table, Channel: sub.Channel, Trigger: name, State: state,
		})
	}

	if fn.exists && fn.owned {
		orphans, err := managedTriggers(ctx, m.conn, fn.oid)
		if err != nil {
			return report, err
		}
		for _, o := range orphans {
			if _, ok := desired[o.name]; ok {
				continue
			}
			report.Objects = append(report.Objects, ObjectStatus{
				Table: o.qualified(), Trigger: o.name, State: StateOrphan,
			})
		}
	}
	return report, nil
}

// Remove drops every managed trigger attached to the managed function, then the
// function, leaving unrelated objects in place.
func (m *Manager) Remove(ctx context.Context, t Target) (Report, error) {
	if strings.TrimSpace(t.Schema) == "" {
		return Report{}, fmt.Errorf("source %q: triggers.schema is required", t.SourceID)
	}
	report := Report{Source: t.SourceID, Schema: t.Schema, Warnings: t.Warnings}

	fn, err := inspectFunction(ctx, m.conn, t.Schema)
	if err != nil {
		return report, err
	}
	if !fn.exists {
		report.Warnings = append(report.Warnings, "no managed trigger function was found")
		return report, nil
	}
	if !fn.owned {
		return report, fmt.Errorf("source %q: function %s.%s() exists but is not owned by Metricule; refusing to remove",
			t.SourceID, t.Schema, FunctionName)
	}

	managed, err := managedTriggers(ctx, m.conn, fn.oid)
	if err != nil {
		return report, err
	}

	tx, err := m.conn.Begin(ctx)
	if err != nil {
		return report, fmt.Errorf("begin trigger removal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	kept := 0
	for _, tr := range managed {
		if !tr.owned {
			// Cannot happen for objects this package creates, but never remove
			// something the marker does not claim (§9.3).
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("trigger %s on %s is not managed; leaving it in place", tr.name, tr.qualified()))
			kept++
			continue
		}
		stmt := dropTriggerSQL(tr.schema, tr.table, tr.name)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return report, fmt.Errorf("drop trigger %s on %s: %w", tr.name, tr.qualified(), err)
		}
		report.Actions = append(report.Actions, Action{
			Kind: "drop", Object: "trigger " + tr.name + " on " + tr.qualified(),
			Table: tr.qualified(), Statements: []string{stmt},
		})
	}

	if kept == 0 {
		stmt := dropFunctionSQL(t.Schema)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return report, fmt.Errorf("drop function %s.%s(): %w", t.Schema, FunctionName, err)
		}
		report.Actions = append(report.Actions, Action{
			Kind: "drop", Object: "function " + t.Schema + "." + FunctionName + "()",
			Statements: []string{stmt},
		})
	} else {
		report.Warnings = append(report.Warnings,
			"function retained because unrelated triggers still depend on it")
	}

	if err := tx.Commit(ctx); err != nil {
		return report, fmt.Errorf("commit trigger removal: %w", err)
	}
	return report, nil
}

// buildPlan inspects the catalog and emits the DDL needed to converge.
func buildPlan(ctx context.Context, q querier, t Target) (Report, error) {
	if err := t.validate(); err != nil {
		return Report{}, err
	}
	report := Report{Source: t.SourceID, Schema: t.Schema, Warnings: t.Warnings}

	present, err := schemaPresent(ctx, q, t.Schema)
	if err != nil {
		return report, err
	}
	if !present {
		report.Actions = append(report.Actions, Action{
			Kind: "create", Object: "schema " + t.Schema,
			Statements: []string{createSchemaSQL(t.Schema)},
		})
	}

	fn, err := inspectFunction(ctx, q, t.Schema)
	if err != nil {
		return report, err
	}
	switch {
	case !fn.exists:
		report.Actions = append(report.Actions, Action{
			Kind: "create", Object: "function " + t.Schema + "." + FunctionName + "()",
			Statements: []string{createFunctionSQL(t.Schema), commentFunctionSQL(t.Schema)},
		})
	case !fn.owned:
		return report, fmt.Errorf("source %q: function %s.%s() exists but is not owned by Metricule; refusing to overwrite",
			t.SourceID, t.Schema, FunctionName)
	case fn.comment != functionMarker:
		// Older marker: converge the definition without touching foreign objects.
		report.Actions = append(report.Actions, Action{
			Kind: "replace", Object: "function " + t.Schema + "." + FunctionName + "()",
			Statements: []string{createFunctionSQL(t.Schema), commentFunctionSQL(t.Schema)},
		})
	default:
		report.Unchanged++
		report.Actions = append(report.Actions, Action{
			Kind: "unchanged", Object: "function " + t.Schema + "." + FunctionName + "()",
		})
	}

	desired := map[string]struct{}{}
	for _, sub := range sortedSubscriptions(t.Subscriptions) {
		tableSchema, tableName, err := splitTable(sub.Table)
		if err != nil {
			return report, fmt.Errorf("source %q: %w", t.SourceID, err)
		}
		kind, err := relationKind(ctx, q, tableSchema, tableName)
		if err != nil {
			return report, err
		}
		switch kind {
		case "":
			return report, fmt.Errorf("source %q: table %q does not exist", t.SourceID, sub.Table)
		case "r", "p":
		default:
			return report, fmt.Errorf("source %q: %q is not a table (relation kind %q)", t.SourceID, sub.Table, kind)
		}

		name := TriggerName(sub.Table, sub.Channel)
		desired[name] = struct{}{}
		tr, err := inspectTrigger(ctx, q, tableSchema, tableName, name)
		if err != nil {
			return report, err
		}

		clause, err := eventClause(sub.Events)
		if err != nil {
			return report, fmt.Errorf("source %q: %w", t.SourceID, err)
		}
		hash := subscriptionHash(sub)
		object := "trigger " + name + " on " + sub.Table

		switch {
		case !tr.exists:
			report.Actions = append(report.Actions, Action{
				Kind: "create", Object: object, Table: sub.Table, Channel: sub.Channel,
				Statements: []string{
					createTriggerSQL(t.Schema, tableSchema, tableName, name, clause, sub.Channel),
					commentTriggerSQL(tableSchema, tableName, name, hash),
				},
			})
		case !tr.owned:
			return report, fmt.Errorf("source %q: %s exists but is not owned by Metricule; refusing to overwrite",
				t.SourceID, object)
		case tr.hash == hash:
			report.Unchanged++
			report.Actions = append(report.Actions, Action{
				Kind: "unchanged", Object: object, Table: sub.Table, Channel: sub.Channel,
			})
		default:
			report.Actions = append(report.Actions, Action{
				Kind: "replace", Object: object, Table: sub.Table, Channel: sub.Channel,
				Statements: []string{
					dropTriggerSQL(tableSchema, tableName, name),
					createTriggerSQL(t.Schema, tableSchema, tableName, name, clause, sub.Channel),
					commentTriggerSQL(tableSchema, tableName, name, hash),
				},
			})
		}
	}

	// Objects from a previous configuration are still managed, so apply converges
	// by dropping them. A user trigger that merely calls the function is left alone.
	if fn.exists && fn.owned {
		managed, err := managedTriggers(ctx, q, fn.oid)
		if err != nil {
			return report, err
		}
		for _, tr := range managed {
			if _, ok := desired[tr.name]; ok {
				continue
			}
			if !tr.owned {
				report.Warnings = append(report.Warnings,
					fmt.Sprintf("trigger %s on %s calls the managed function but is not marked; leaving it in place",
						tr.name, tr.qualified()))
				continue
			}
			stmt := dropTriggerSQL(tr.schema, tr.table, tr.name)
			report.Actions = append(report.Actions, Action{
				Kind: "drop", Object: "orphan trigger " + tr.name + " on " + tr.qualified(),
				Table: tr.qualified(), Statements: []string{stmt},
			})
		}
	}
	return report, nil
}

// sortedSubscriptions orders by table then channel for deterministic plans.
func sortedSubscriptions(subs []Subscription) []Subscription {
	out := append([]Subscription(nil), subs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Channel < out[j].Channel
	})
	return out
}

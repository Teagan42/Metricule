package triggers

import (
	"context"
	"strings"
	"testing"
)

func TestTriggerNameIsStableAndBounded(t *testing.T) {
	a := TriggerName("public.jobs", "metricule_jobs_changed")
	b := TriggerName("public.jobs", "metricule_jobs_changed")
	if a != b {
		t.Fatalf("name is not stable: %q then %q", a, b)
	}
	if !strings.HasPrefix(a, triggerPrefix) {
		t.Errorf("name = %q, want prefix %q", a, triggerPrefix)
	}
	if len(a) > 63 {
		t.Errorf("name = %q is %d bytes, over PostgreSQL's 63-byte limit", a, len(a))
	}
	if TriggerName("public.jobs", "one") == TriggerName("public.jobs", "two") {
		t.Error("different channels produced the same trigger name")
	}
	if TriggerName("public.jobs", "c") == TriggerName("public.users", "c") {
		t.Error("different tables produced the same trigger name")
	}
}

func TestSplitTableRequiresQualification(t *testing.T) {
	if _, _, err := splitTable("jobs"); err == nil {
		t.Error("unqualified table accepted")
	}
	if _, _, err := splitTable("public.jobs.extra"); err == nil {
		t.Error("over-qualified table accepted")
	}
	schema, name, err := splitTable("public.jobs")
	if err != nil || schema != "public" || name != "jobs" {
		t.Errorf("splitTable = %q, %q, %v", schema, name, err)
	}
}

func TestEventClauseDefaultsAndValidates(t *testing.T) {
	if got, _ := eventClause(nil); got != "INSERT OR UPDATE OR DELETE" {
		t.Errorf("default clause = %q", got)
	}
	if got, _ := eventClause([]string{"update", "insert", "insert"}); got != "INSERT OR UPDATE" {
		t.Errorf("clause = %q, want deterministic order without duplicates", got)
	}
	if _, err := eventClause([]string{"truncate"}); err == nil {
		t.Error("unsupported event accepted")
	}
}

func TestGeneratedSQLQuotesIdentifiersAndLiterals(t *testing.T) {
	create := createTriggerSQL(`odd schema`, "public", `jobs"quoted`, `trigger"name`, "INSERT", `chan'nel`)
	for _, want := range []string{
		`"odd schema"."metricule_notify_change"`,
		`"public"."jobs""quoted"`,
		`"trigger""name"`,
		`'chan''nel'`,
		`FOR EACH STATEMENT`,
	} {
		if !strings.Contains(create, want) {
			t.Errorf("CREATE TRIGGER missing %q:\n%s", want, create)
		}
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	hash := subscriptionHash(Subscription{Table: "public.jobs", Channel: "c", Events: []string{"insert"}})
	comment := triggerMarker(hash)
	if !owned(comment) {
		t.Errorf("marker %q is not recognized", comment)
	}
	if got := markerHash(comment); got != hash {
		t.Errorf("markerHash = %q, want %q", got, hash)
	}
	if owned("something else") {
		t.Error("a foreign comment was treated as managed")
	}
}

// The subscription hash changes when the trigger definition must change, and not
// otherwise.
func TestSubscriptionHashTracksTheDefinition(t *testing.T) {
	base := Subscription{Table: "public.jobs", Channel: "c", Events: []string{"insert"}}
	if first, second := subscriptionHash(base), subscriptionHash(base); first != second {
		t.Fatalf("hash is not deterministic: %q then %q", first, second)
	}
	changedEvents := base
	changedEvents.Events = []string{"update"}
	changedChannel := base
	changedChannel.Channel = "other"
	changedTable := base
	changedTable.Table = "public.users"

	for name, other := range map[string]Subscription{
		"events": changedEvents, "channel": changedChannel, "table": changedTable,
	} {
		if subscriptionHash(base) == subscriptionHash(other) {
			t.Errorf("hash did not change with %s", name)
		}
	}
}

func TestTargetValidation(t *testing.T) {
	valid := Target{
		SourceID: "appdb", Schema: "metricule",
		Subscriptions: []Subscription{{Table: "public.jobs", Channel: "changes"}},
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}

	cases := map[string]Target{
		"missing schema": {SourceID: "appdb", Subscriptions: valid.Subscriptions},
		"unqualified":    {SourceID: "appdb", Schema: "m", Subscriptions: []Subscription{{Table: "jobs", Channel: "c"}}},
		"no channel":     {SourceID: "appdb", Schema: "m", Subscriptions: []Subscription{{Table: "public.jobs"}}},
		"long channel": {SourceID: "appdb", Schema: "m", Subscriptions: []Subscription{{
			Table: "public.jobs", Channel: strings.Repeat("c", 64),
		}}},
		"bad event": {SourceID: "appdb", Schema: "m", Subscriptions: []Subscription{{
			Table: "public.jobs", Channel: "c", Events: []string{"truncate"},
		}}},
	}
	for name, target := range cases {
		if err := target.validate(); err == nil {
			t.Errorf("%s: invalid target accepted", name)
		}
	}
}

// Apply refuses to provision unless the source explicitly opted in.
func TestApplyRequiresOptIn(t *testing.T) {
	m := &Manager{}
	_, err := m.Apply(context.Background(), Target{SourceID: "appdb", Schema: "metricule"})
	if err == nil || !strings.Contains(err.Error(), "triggers.manage is false") {
		t.Fatalf("Apply error = %v, want an opt-in refusal", err)
	}
}

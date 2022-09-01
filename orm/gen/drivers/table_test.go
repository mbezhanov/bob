package drivers

import (
	"reflect"
	"testing"
)

func TestGetTable(t *testing.T) {
	t.Parallel()

	tables := []Table{
		{Name: "one"},
	}

	tbl := GetTable(tables, "one")

	if tbl.Name != "one" {
		t.Error("didn't get column")
	}
}

func TestGetTableMissing(t *testing.T) {
	t.Parallel()

	tables := []Table{
		{Name: "one"},
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected a panic failure")
		}
	}()

	GetTable(tables, "missing")
}

func TestGetColumn(t *testing.T) {
	t.Parallel()

	table := Table{
		Columns: []Column{
			{Name: "one"},
		},
	}

	c := table.GetColumn("one")

	if c.Name != "one" {
		t.Error("didn't get column")
	}
}

func TestGetColumnMissing(t *testing.T) {
	t.Parallel()

	table := Table{
		Columns: []Column{
			{Name: "one"},
		},
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected a panic failure")
		}
	}()

	table.GetColumn("missing")
}

func TestCanSoftDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Can     bool
		Columns []Column
	}{
		{true, []Column{
			{Name: "deleted_at", Type: "null.Time"},
		}},
		{false, []Column{
			{Name: "deleted_at", Type: "time.Time"},
		}},
		{false, []Column{
			{Name: "deleted_at", Type: "int"},
		}},
		{false, nil},
	}

	for i, test := range tests {
		table := Table{
			Columns: test.Columns,
		}

		if got := table.CanSoftDelete("deleted_at"); got != test.Can {
			t.Errorf("%d) wrong: %t", i, got)
		}
	}
}

func TestTablesFromList(t *testing.T) {
	t.Parallel()

	if TablesFromList(nil) != nil {
		t.Error("expected a shortcut to getting nil back")
	}

	if got := TablesFromList([]string{"a.b", "b", "c.d"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Error("list was wrong:", got)
	}
}

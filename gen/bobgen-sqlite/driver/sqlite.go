package driver

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/aarondl/opt/null"
	helpers "github.com/stephenafamo/bob/gen/bobgen-helpers"
	"github.com/stephenafamo/bob/gen/drivers"
	"github.com/stephenafamo/scan"
	"github.com/stephenafamo/scan/stdscan"
	"github.com/volatiletech/strmangle"
	_ "modernc.org/sqlite"
)

type (
	Interface = drivers.Interface[any]
	DBInfo    = drivers.DBInfo[any]
)

func New(config Config) Interface {
	if config.DriverName == "" {
		config.DriverName = "modernc.org/sqlite"
	}
	return &driver{config: config}
}

type Config struct {
	// The database connection string
	DSN string
	// The database schemas to generate models for
	// a map of the schema name to the DSN
	Attach map[string]string
	// The name of this schema will not be included in the generated models
	// a context value can then be used to set the schema at runtime
	// useful for multi-tenant setups
	SharedSchema string `yaml:"shared_schema"`
	// List of tables that will be included. Others are ignored
	Only map[string][]string
	// List of tables that will be should be ignored. Others are included
	Except map[string][]string
	// Which `database/sql` driver to use (the full module name)
	DriverName string `yaml:"driver_name"`

	// Used in main.go

	// The name of the folder to output the models package to
	Output string
	// The name you wish to assign to your generated models package
	Pkgname   string
	NoFactory bool `yaml:"no_factory"`
}

// driver holds the database connection string and a handle
// to the database connection.
type driver struct {
	config Config
	conn   *sql.DB
}

func (d *driver) Dialect() string {
	return "sqlite"
}

func (d *driver) Destination() string {
	return d.config.Output
}

func (d *driver) PackageName() string {
	return d.config.Pkgname
}

func (d *driver) Capabilities() drivers.Capabilities {
	return drivers.Capabilities{}
}

func (d *driver) Types() drivers.Types {
	return helpers.Types()
}

// Assemble all the information we need to provide back to the driver
func (d *driver) Assemble(ctx context.Context) (*DBInfo, error) {
	var err error

	if d.config.SharedSchema == "" {
		d.config.SharedSchema = "main"
	}

	if d.config.DSN == "" {
		return nil, fmt.Errorf("database dsn is not set")
	}

	d.conn, err = sql.Open("sqlite", d.config.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer d.conn.Close()

	for schema, dsn := range d.config.Attach {
		_, err = d.conn.ExecContext(ctx, fmt.Sprintf("attach database '%s' as %s", dsn, schema))
		if err != nil {
			return nil, fmt.Errorf("could not attach %q: %w", schema, err)
		}
	}

	tables, err := d.tables(ctx)
	if err != nil {
		return nil, err
	}

	dbinfo := &DBInfo{
		DriverName: d.config.DriverName,
		Tables:     tables,
	}

	return dbinfo, nil
}

func (d *driver) buildQuery(schema string) (string, []any) {
	var args []any
	query := fmt.Sprintf(`SELECT name FROM %q.sqlite_schema WHERE name NOT LIKE 'sqlite_%%' AND type IN ('table', 'view')`, schema)

	tableFilter := drivers.ParseTableFilter(d.config.Only, d.config.Except)

	if len(tableFilter.Only) > 0 {
		var subqueries []string
		stringPatterns, regexPatterns := tableFilter.ClassifyPatterns(tableFilter.Only)
		include := make([]string, 0, len(stringPatterns))
		for _, name := range stringPatterns {
			if (schema == "main" && !strings.Contains(name, ".")) || strings.HasPrefix(name, schema+".") {
				include = append(include, strings.TrimPrefix(name, schema+"."))
			}
		}
		if len(include) > 0 {
			subqueries = append(subqueries, fmt.Sprintf("name in (%s)", strmangle.Placeholders(true, len(include), 1, 1)))
			for _, w := range include {
				args = append(args, w)
			}
		}
		if len(regexPatterns) > 0 {
			subqueries = append(subqueries, fmt.Sprintf("name regexp (%s)", strmangle.Placeholders(true, 1, len(args)+1, 1)))
			args = append(args, strings.Join(regexPatterns, "|"))
		}
		if len(subqueries) > 0 {
			query += fmt.Sprintf(" and (%s)", strings.Join(subqueries, " or "))
		}
	}

	if len(tableFilter.Except) > 0 {
		var subqueries []string
		stringPatterns, regexPatterns := tableFilter.ClassifyPatterns(tableFilter.Except)
		exclude := make([]string, 0, len(tableFilter.Except))
		for _, name := range stringPatterns {
			if (schema == "main" && !strings.Contains(name, ".")) || strings.HasPrefix(name, schema+".") {
				exclude = append(exclude, strings.TrimPrefix(name, schema+"."))
			}
		}
		if len(exclude) > 0 {
			subqueries = append(subqueries, fmt.Sprintf("name not in (%s)", strmangle.Placeholders(true, len(exclude), 1+len(args), 1)))
			for _, w := range exclude {
				args = append(args, w)
			}
		}
		if len(regexPatterns) > 0 {
			subqueries = append(subqueries, fmt.Sprintf("name not regexp (%s)", strmangle.Placeholders(true, 1, len(args)+1, 1)))
			args = append(args, strings.Join(regexPatterns, "|"))
		}
		if len(subqueries) > 0 {
			query += fmt.Sprintf(" and (%s)", strings.Join(subqueries, " and "))
		}
	}

	query += ` ORDER BY type, name`

	return query, args
}

func (d *driver) tables(ctx context.Context) ([]drivers.Table, error) {
	mainQuery, mainArgs := d.buildQuery("main")
	mainTables, err := stdscan.All(ctx, d.conn, scan.SingleColumnMapper[string], mainQuery, mainArgs...)
	if err != nil {
		return nil, err
	}

	allTables := make([]drivers.Table, len(mainTables))
	for i, name := range mainTables {
		allTables[i], err = d.getTable(ctx, "main", name)
		if err != nil {
			return nil, err
		}
	}

	for schema := range d.config.Attach {
		schemaQuery, schemaArgs := d.buildQuery(schema)
		tables, err := stdscan.All(ctx, d.conn, scan.SingleColumnMapper[string], schemaQuery, schemaArgs...)
		if err != nil {
			return nil, err
		}

		for _, name := range tables {
			table, err := d.getTable(ctx, schema, name)
			if err != nil {
				return nil, err
			}
			allTables = append(allTables, table)
		}
	}

	return allTables, nil
}

func (d driver) getTable(ctx context.Context, schema, name string) (drivers.Table, error) {
	var err error

	table := drivers.Table{
		Key:    d.key(schema, name),
		Schema: d.schema(schema),
		Name:   name,
	}

	tinfo, err := d.tableInfo(ctx, schema, name)
	if err != nil {
		return table, err
	}

	table.Columns, err = d.columns(ctx, schema, name, tinfo)
	if err != nil {
		return table, err
	}

	table.Constraints.Primary = d.primaryKey(schema, name, tinfo)
	table.Constraints.Foreign, err = d.foreignKeys(ctx, schema, name)
	if err != nil {
		return table, err
	}

	table.Constraints.Uniques, err = d.uniques(ctx, schema, name)
	if err != nil {
		return table, err
	}

	table.Indexes, err = d.indexes(ctx, schema, name)
	if err != nil {
		return table, err
	}

	return table, nil
}

// Columns takes a table name and attempts to retrieve the table information
// from the database. It retrieves the column names
// and column types and returns those as a []Column after TranslateColumnType()
// converts the SQL types to Go types, for example: "varchar" to "string"
func (d driver) columns(ctx context.Context, schema, tableName string, tinfo []info) ([]drivers.Column, error) {
	var columns []drivers.Column //nolint:prealloc

	//nolint:gosec
	query := fmt.Sprintf("SELECT 1 FROM '%s'.sqlite_master WHERE type = 'table' AND name = ? AND sql LIKE '%%AUTOINCREMENT%%'", schema)
	result, err := d.conn.QueryContext(ctx, query, tableName)
	if err != nil {
		return nil, fmt.Errorf("autoincr query: %w", err)
	}
	tableHasAutoIncr := result.Next()
	if err := result.Close(); err != nil {
		return nil, err
	}

	nPkeys := 0
	for _, column := range tinfo {
		if column.Pk != 0 {
			nPkeys++
		}
	}

	for _, colInfo := range tinfo {
		column := drivers.Column{
			Name:     colInfo.Name,
			DBType:   strings.ToUpper(colInfo.Type),
			Nullable: !colInfo.NotNull && colInfo.Pk < 1,
		}

		isPrimaryKeyInteger := colInfo.Pk == 1 && column.DBType == "INTEGER"
		// This is special behavior noted in the sqlite documentation.
		// An integer primary key becomes synonymous with the internal ROWID
		// and acts as an auto incrementing value. Although there's important
		// differences between using the keyword AUTOINCREMENT and this inferred
		// version, they don't matter here so just masquerade as the same thing as
		// above.
		autoIncr := isPrimaryKeyInteger && (tableHasAutoIncr || nPkeys == 1)

		// See: https://github.com/sqlite/sqlite/blob/91f621531dc1cb9ba5f6a47eb51b1de9ed8bdd07/src/pragma.c#L1165
		column.Generated = colInfo.Hidden == 2 || colInfo.Hidden == 3

		if colInfo.DefaultValue.Valid {
			column.Default = colInfo.DefaultValue.String
		} else if autoIncr {
			column.Default = "auto_increment"
		} else if column.Generated {
			column.Default = "auto_generated"
		}

		if column.Nullable && column.Default == "" {
			column.Default = "NULL"
		}

		columns = append(columns, d.translateColumnType(column))
	}

	return columns, nil
}

func (s driver) tableInfo(ctx context.Context, schema, tableName string) ([]info, error) {
	var ret []info
	rows, err := s.conn.QueryContext(ctx, fmt.Sprintf("PRAGMA '%s'.table_xinfo('%s')", schema, tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		tinfo := info{}
		if err := rows.Scan(&tinfo.Cid, &tinfo.Name, &tinfo.Type, &tinfo.NotNull, &tinfo.DefaultValue, &tinfo.Pk, &tinfo.Hidden); err != nil {
			return nil, fmt.Errorf("unable to scan for table %s: %w", tableName, err)
		}

		ret = append(ret, tinfo)
	}
	return ret, nil
}

// primaryKey looks up the primary key for a table.
func (s driver) primaryKey(schema, tableName string, tinfo []info) *drivers.PrimaryKey {
	var cols []string
	for _, c := range tinfo {
		if c.Pk > 0 {
			cols = append(cols, c.Name)
		}
	}

	if len(cols) == 0 {
		return nil
	}

	return &drivers.PrimaryKey{
		Name:    fmt.Sprintf("pk_%s_%s", schema, tableName),
		Columns: cols,
	}
}

func (d driver) skipKey(table, column string) bool {
	if len(d.config.Only) > 0 {
		// check if the table is listed at all
		filter, ok := d.config.Only[table]
		if !ok {
			return true
		}

		// check if the column is listed
		if len(filter) == 0 {
			return false
		}

		for _, filteredCol := range filter {
			if filteredCol == column {
				return false
			}
		}
		return true
	}

	if len(d.config.Except) > 0 {
		filter, ok := d.config.Except[table]
		if !ok {
			return false
		}

		if len(filter) == 0 {
			return true
		}

		for _, filteredCol := range filter {
			if filteredCol == column {
				return true
			}
		}
	}

	return false
}

// foreignKeys retrieves the foreign keys for a given table name.
func (d driver) foreignKeys(ctx context.Context, schema, tableName string) ([]drivers.ForeignKey, error) {
	rows, err := d.conn.QueryContext(ctx, fmt.Sprintf("PRAGMA '%s'.foreign_key_list('%s')", schema, tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fkeyMap := make(map[int]drivers.ForeignKey)
	for rows.Next() {
		var id, seq int
		var ftable, col string
		var fcolNullable null.Val[string]

		// not used
		var onupdate, ondelete, match string

		err = rows.Scan(&id, &seq, &ftable, &col, &fcolNullable, &onupdate, &ondelete, &match)
		if err != nil {
			return nil, err
		}

		fullFtable := ftable
		if schema != "main" {
			fullFtable = fmt.Sprintf("%s.%s", schema, ftable)
		}

		fcol, _ := fcolNullable.Get()
		if fcol == "" {
			fcol, err = stdscan.One(
				ctx, d.conn, scan.SingleColumnMapper[string],
				fmt.Sprintf("SELECT name FROM '%s'.pragma_table_info('%s') WHERE pk = ?", schema, ftable), seq+1,
			)
			if err != nil {
				return nil, fmt.Errorf("could not find column %q in table %q: %w", col, ftable, err)
			}
		}

		if d.skipKey(fullFtable, fcol) {
			continue
		}

		fkeyMap[id] = drivers.ForeignKey{
			Name:           fmt.Sprintf("fk_%s_%d", tableName, id),
			Columns:        append(fkeyMap[id].Columns, col),
			ForeignTable:   d.key(schema, ftable),
			ForeignColumns: append(fkeyMap[id].ForeignColumns, fcol),
		}
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	fkeys := make([]drivers.ForeignKey, 0, len(fkeyMap))

	for _, fkey := range fkeyMap {
		fkeys = append(fkeys, fkey)
	}

	sort.Slice(fkeys, func(i, j int) bool {
		return fkeys[i].Name < fkeys[j].Name
	})

	return fkeys, nil
}

// uniques retrieves the unique keys for a given table name.
func (d driver) uniques(ctx context.Context, schema, tableName string) ([]drivers.Constraint, error) {
	rows, err := d.conn.QueryContext(ctx, fmt.Sprintf("PRAGMA '%s'.index_list('%s')", schema, tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string

		err = rows.Scan(&seq, &name, &unique, &origin, &partial)
		if err != nil {
			return nil, err
		}

		// Index must be created by a unique constraint
		if origin != "u" {
			continue
		}

		indexes = append(indexes, name)
	}
	rows.Close()

	if err = rows.Err(); err != nil {
		return nil, err
	}

	uniques := make([]drivers.Constraint, len(indexes))
	for i, index := range indexes {
		uniques[i], err = d.getUniqueIndex(ctx, schema, index)
		if err != nil {
			return nil, err
		}
	}

	return uniques, nil
}

func (d driver) getUniqueIndex(ctx context.Context, schema, index string) (drivers.Constraint, error) {
	unique := drivers.Constraint{Name: index}

	rows, err := d.conn.QueryContext(ctx, fmt.Sprintf("PRAGMA '%s'.index_info('%s')", schema, index))
	if err != nil {
		return unique, err
	}
	defer rows.Close()

	for rows.Next() {
		var seq, cid int
		var name sql.NullString

		err = rows.Scan(&seq, &cid, &name)
		if err != nil {
			return unique, err
		}

		// Index must be created by a unique constraint
		if !name.Valid {
			continue
		}

		unique.Columns = append(unique.Columns, name.String)
	}

	return unique, nil
}

type info struct {
	Cid          int
	Name         string
	Type         string
	NotNull      bool
	DefaultValue sql.NullString
	Pk           int
	Hidden       int
}

func (d *driver) key(schema string, table string) string {
	key := table
	if schema != "" && schema != d.config.SharedSchema {
		key = schema + "." + table
	}

	return key
}

func (d *driver) schema(schema string) string {
	if schema == d.config.SharedSchema {
		return ""
	}

	return schema
}

// TranslateColumnType converts sqlite database types to Go types, for example
// "varchar" to "string" and "bigint" to "int64". It returns this parsed data
// as a Column object.
// https://sqlite.org/datatype3.html
func (driver) translateColumnType(c drivers.Column) drivers.Column {
	switch c.DBType {
	case "TINYINT", "INT8":
		c.Type = "int8"
	case "SMALLINT", "INT2":
		c.Type = "int16"
	case "MEDIUMINT":
		c.Type = "int32"
	case "INT", "INTEGER":
		c.Type = "int32"
	case "BIGINT":
		c.Type = "int64"
	case "UNSIGNED BIG INT":
		c.Type = "uint64"
	case "CHARACTER", "VARCHAR", "VARYING CHARACTER", "NCHAR",
		"NATIVE CHARACTER", "NVARCHAR", "TEXT", "CLOB":
		c.Type = "string"
	case "BLOB":
		c.Type = "[]byte"
	case "FLOAT", "REAL":
		c.Type = "float32"
	case "DOUBLE", "DOUBLE PRECISION":
		c.Type = "float64"
	case "NUMERIC", "DECIMAL":
		c.Type = "decimal.Decimal"
	case "BOOLEAN":
		c.Type = "bool"
	case "DATE", "DATETIME":
		c.Type = "time.Time"
	case "JSON":
		c.Type = "types.JSON[json.RawMessage]"

	default:
		c.Type = "string"
	}

	return c
}

func (d *driver) indexes(ctx context.Context, schema, tableName string) ([]drivers.Index, error) {
	//nolint:gosec
	query := fmt.Sprintf("SELECT name FROM '%s'.pragma_index_list('%s') ORDER BY name ASC", schema, tableName)
	rows, err := d.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexNames []string
	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		if err != nil {
			return nil, err
		}
		indexNames = append(indexNames, name)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	indexes := make([]drivers.Index, len(indexNames))
	for i, indexName := range indexNames {
		indexes[i], err = d.getIndexInformation(ctx, schema, tableName, indexName)
		if err != nil {
			return nil, err
		}
	}

	return indexes, nil
}

func (d *driver) getIndexInformation(ctx context.Context, schema, tableName, indexName string) (drivers.Index, error) {
	var index drivers.Index
	index.Name = indexName

	//nolint:gosec
	query := fmt.Sprintf("SELECT name FROM '%s'.pragma_index_info('%s') ORDER BY seqno ASC", schema, indexName)
	rows, err := d.conn.QueryContext(ctx, query)
	if err != nil {
		return index, err
	}
	defer rows.Close()

	exprCols := make(map[int]struct{})
	for seqno := 0; rows.Next(); seqno++ {
		var name null.Val[string]
		err = rows.Scan(&name)
		if err != nil {
			return index, err
		}
		if name.IsSet() {
			index.Columns = append(index.Columns, name.GetOrZero())
		} else {
			exprCols[seqno] = struct{}{}
		}
	}

	if len(exprCols) > 0 {
		index.Expressions, err = d.extractIndexExpressions(ctx, schema, tableName, indexName, exprCols)
		if err != nil {
			return index, err
		}
	}

	if err = rows.Err(); err != nil {
		return index, err
	}

	return index, nil
}

func (d driver) extractIndexExpressions(ctx context.Context, schema, tableName, indexName string, exprCols map[int]struct{}) ([]string, error) {
	var ddl string
	var expressions []string //nolint:prealloc
	//nolint:gosec
	query := fmt.Sprintf("SELECT sql FROM '%s'.sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = ?", schema)
	result := d.conn.QueryRowContext(ctx, query, indexName, tableName)
	err := result.Scan(&ddl)
	if err != nil {
		return expressions, fmt.Errorf("failed retrieving index DDL statement: %w", err)
	}
	// We're following the parsing logic from the `intckParseCreateIndex` function in the SQLite source code.
	// 1. https://github.com/sqlite/sqlite/blob/1d8cde9d56d153767e98595c4b015221864ef0e7/ext/intck/sqlite3intck.c#L363
	// 2. https://www.sqlite.org/lang_createindex.html

	// skip forward until the first "(" token
	i := strings.Index(ddl, "(")
	if i == -1 {
		return expressions, fmt.Errorf("failed locating first column: %w", err)
	}
	ddl = ddl[i+1:]
	// discard the WHERE clause fragment (if one exists)
	i = strings.LastIndex(ddl, ")")
	if i == -1 {
		return expressions, fmt.Errorf("failed locating last column: %w", err)
	}
	ddl = ddl[:i]
	// organize column definitions into a list
	colDefs := d.splitColumnDefinitions(ddl)

	for seqno, expression := range colDefs {
		if _, ok := exprCols[seqno]; !ok {
			// this index column references a regular column rather than an expression, so we skip the extraction.
			continue
		}
		expressions = append(expressions, strings.TrimSpace(expression))
	}

	return expressions, nil
}

// splitColumnDefinitions performs an intelligent split of the DDL part defining the index columns.
//
// We cannot perform a simple `strings.Split(ddl, ",")` as `ddl` could contain functional expressions, i.e.:
//
//	sql  := CREATE INDEX idx ON test (col1, (col2 + col3), (POW(col3, 2)));
//	ddl  := "col1, (col2 + col3), (POW(col3, 2))"
//	defs := []string{"col1", "(col2 + col3)", "(POW(col3, 2))"}
func (d driver) splitColumnDefinitions(ddl string) []string {
	var defs []string
	var i, pOpen int

	for j := 0; j < len(ddl); j++ {
		if ddl[j] == '(' {
			pOpen++
		}
		if ddl[j] == ')' {
			pOpen--
		}
		if pOpen == 0 && ddl[j] == ',' {
			defs = append(defs, ddl[i:j])
			i = j + 1
		}
	}

	if i < len(ddl) {
		defs = append(defs, ddl[i:])
	}

	return defs
}

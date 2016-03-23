package dbdrivers

import (
	"database/sql"
	"fmt"

	// Import the postgres driver
	_ "github.com/lib/pq"
)

// PostgresDriver holds the database connection string and a handle
// to the database connection.
type PostgresDriver struct {
	connStr string
	dbConn  *sql.DB
}

// NewPostgresDriver takes the database connection details as parameters and
// returns a pointer to a PostgresDriver object. Note that it is required to
// call PostgresDriver.Open() and PostgresDriver.Close() to open and close
// the database connection once an object has been obtained.
func NewPostgresDriver(user, pass, dbname, host string, port int) *PostgresDriver {
	driver := PostgresDriver{
		connStr: fmt.Sprintf("user=%s password=%s dbname=%s host=%s port=%d",
			user, pass, dbname, host, port),
	}

	return &driver
}

// Open opens the database connection using the connection string
func (p *PostgresDriver) Open() error {
	var err error
	p.dbConn, err = sql.Open("postgres", p.connStr)
	if err != nil {
		return err
	}

	return nil
}

// Close closes the database connection
func (p *PostgresDriver) Close() {
	p.dbConn.Close()
}

// Tables returns the table metadata for the given tables, or all tables if
// no tables are provided.
func (p *PostgresDriver) Tables(names ...string) ([]Table, error) {
	var err error
	if len(names) == 0 {
		if names, err = p.tableNames(); err != nil {
			return nil, err
		}
	}

	var tables []Table
	for _, name := range names {
		t := Table{Name: name}

		if t.Columns, err = p.columns(name); err != nil {
			return nil, err
		}

		if t.PKey, err = p.primaryKeyInfo(name); err != nil {
			return nil, err
		}

		if t.FKeys, err = p.foreignKeyInfo(name); err != nil {
			return nil, err
		}

		tables = append(tables, t)
	}

	return tables, nil
}

// tableNames connects to the postgres database and
// retrieves all table names from the information_schema where the
// table schema is public. It excludes common migration tool tables
// such as gorp_migrations
func (p *PostgresDriver) tableNames() ([]string, error) {
	var names []string

	rows, err := p.dbConn.Query(`select table_name from
    information_schema.tables where table_schema='public'
    and table_name <> 'gorp_migrations'`)

	if err != nil {
		return nil, err
	}

	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}

	return names, nil
}

// columns takes a table name and attempts to retrieve the table information
// from the database information_schema.columns. It retrieves the column names
// and column types and returns those as a []Column after TranslateColumnType()
// converts the SQL types to Go types, for example: "varchar" to "string"
func (p *PostgresDriver) columns(tableName string) ([]Column, error) {
	var columns []Column

	rows, err := p.dbConn.Query(`
		SELECT c.column_name, c.data_type, c.is_nullable,
		CASE WHEN pk.column_name IS NOT NULL THEN 'PRIMARY KEY' ELSE '' END AS KeyType
		FROM information_schema.columns c
		LEFT JOIN (
		  SELECT ku.table_name, ku.column_name
		  FROM information_schema.table_constraints AS tc
		  INNER JOIN information_schema.key_column_usage AS ku
		    ON tc.constraint_type = 'PRIMARY KEY'
		    AND tc.constraint_name = ku.constraint_name
		) pk
		ON c.table_name = pk.table_name
		AND c.column_name = pk.column_name
		WHERE c.table_name=$1
	`, tableName)

	if err != nil {
		return nil, err
	}

	defer rows.Close()
	for rows.Next() {
		var colName, colType, isNullable, isPrimary string
		if err := rows.Scan(&colName, &colType, &isNullable, &isPrimary); err != nil {
			return nil, err
		}
		column := p.TranslateColumnType(Column{
			Name:         colName,
			Type:         colType,
			IsNullable:   isNullable == "YES",
			IsPrimaryKey: isPrimary == "PRIMARY KEY",
		})
		columns = append(columns, column)
	}

	return columns, nil
}

// primaryKeyInfo looks up the primary key for a table.
func (p *PostgresDriver) primaryKeyInfo(tableName string) (*PrimaryKey, error) {
	pkey := &PrimaryKey{}
	var err error

	query := ``

	row := p.dbConn.QueryRow(query, tableName)
	if err = row.Scan(&pkey.Name); err != nil {
		return nil, err
	}

	queryColumns := ``

	var rows *sql.Rows
	if rows, err = p.dbConn.Query(queryColumns, tableName); err != nil {
		return nil, err
	}

	for rows.Next() {
		var column string

		err = rows.Scan(&column)
		if err != nil {
			return nil, err
		}
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return pkey, nil
}

// foreignKeyInfo retrieves the foreign keys for a given table name.
func (p *PostgresDriver) foreignKeyInfo(tableName string) ([]ForeignKey, error) {
	var fkeys []ForeignKey

	query := `
	SELECT
    tc.constraint_name,
    kcu.table_name as source_table,
    kcu.column_name as source_column,
    ccu.table_name as dest_table,
    ccu.column_name as dest_column
	FROM information_schema.table_constraints as tc
	JOIN information_schema.key_column_usage as kcu ON tc.constraint_name = kcu.constraint_name
	JOIN information_schema.constraint_column_usage as ccu ON tc.constraint_name = ccu.constraint_name
	WHERE source_table = $1, tc.constraint_type = 'FOREIGN KEY';`

	var rows *sql.Rows
	var err error
	if rows, err = p.dbConn.Query(query, tableName); err != nil {
		return nil, err
	}

	for rows.Next() {
		var fkey ForeignKey
		var sourceTable string

		err = rows.Scan(&fkey.Name, &sourceTable, &fkey.Column, &fkey.ForeignTable, &fkey.ForeignColumn)
		if err != nil {
			return nil, err
		}
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return fkeys, nil
}

// TranslateColumnType converts postgres database types to Go types, for example
// "varchar" to "string" and "bigint" to "int64". It returns this parsed data
// as a Column object.
func (p *PostgresDriver) TranslateColumnType(c Column) Column {
	if c.IsNullable {
		switch c.Type {
		case "bigint", "bigserial", "integer", "smallint", "smallserial", "serial":
			c.Type = "null.Int"
		case "bit", "bit varying", "character", "character varying", "cidr", "inet", "json", "macaddr", "text", "uuid", "xml":
			c.Type = "null.String"
		case "boolean":
			c.Type = "null.Bool"
		case "date", "interval", "time", "timestamp without time zone", "timestamp with time zone":
			c.Type = "null.Time"
		case "double precision", "money", "numeric", "real":
			c.Type = "null.Float"
		default:
			c.Type = "null.String"
		}
	} else {
		switch c.Type {
		case "bigint", "bigserial", "integer", "smallint", "smallserial", "serial":
			c.Type = "int64"
		case "bit", "bit varying", "character", "character varying", "cidr", "inet", "json", "macaddr", "text", "uuid", "xml":
			c.Type = "string"
		case "bytea":
			c.Type = "[]byte"
		case "boolean":
			c.Type = "bool"
		case "date", "interval", "time", "timestamp without time zone", "timestamp with time zone":
			c.Type = "time.Time"
		case "double precision", "money", "numeric", "real":
			c.Type = "float64"
		default:
			c.Type = "string"
		}
	}

	return c
}

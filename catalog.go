package jetadmin

import (
	"context"
	"reflect"

	pg "github.com/go-jet/jet/v2/postgres"
)

// What the generated code does not say about a column, read once from
// information_schema: a jsonb column is a ColumnString and a string field
// to Jet, and an identity or generated column looks like any other.

// columnInfo is a column's entry in the catalog.
type columnInfo struct {
	json      bool // json or jsonb
	generated bool // GENERATED ALWAYS (identity or stored): never written
	defaulted bool // has a default or is an identity
	// defaultExpr is the default's text, 'free'::text or now().
	defaultExpr string
}

type catalog struct {
	columns map[[3]string]columnInfo // schema, table, column
	byTable map[[2]string]columnInfo // table, column, for builders without a schema
}

var (
	isTableSchema        = pg.StringColumn("table_schema")
	isTableName          = pg.StringColumn("table_name")
	isColumnName         = pg.StringColumn("column_name")
	isUDTName            = pg.StringColumn("udt_name")
	isIdentity           = pg.StringColumn("is_identity")
	isIdentityGeneration = pg.StringColumn("identity_generation")
	isGenerated          = pg.StringColumn("is_generated")
	isColumnDefault      = pg.StringColumn("column_default")
	infoColumns          = pg.NewTable("information_schema", "columns", "",
		isTableSchema, isTableName, isColumnName, isUDTName, isIdentity, isIdentityGeneration, isGenerated, isColumnDefault)
)

type catalogRow struct {
	Schema, Table, Column, UDT string
	Identity                   string
	IdentityGeneration         *string
	Generated                  string
	ColumnDefault              *string
}

// ensureCatalog reads the catalog the first time a page needs it. A
// failure is retried on the next request.
func (s *Site) ensureCatalog(ctx context.Context) error {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.catalog != nil {
		return nil
	}
	// The information_schema columns are domains (sql_identifier,
	// yes_or_no), which pgx has no codec for: read them as text.
	stmt := pg.SELECT(
		pg.CAST(isTableSchema).AS_TEXT().AS("schema"),
		pg.CAST(isTableName).AS_TEXT().AS("table"),
		pg.CAST(isColumnName).AS_TEXT().AS("column"),
		pg.CAST(isUDTName).AS_TEXT().AS("udt"),
		pg.CAST(isIdentity).AS_TEXT().AS("identity"),
		pg.CAST(isIdentityGeneration).AS_TEXT().AS("identity_generation"),
		pg.CAST(isGenerated).AS_TEXT().AS("generated"),
		pg.CAST(isColumnDefault).AS_TEXT().AS("column_default"),
	).FROM(infoColumns).WHERE(isTableSchema.NOT_IN(pg.String("pg_catalog"), pg.String("information_schema")))
	query, args := stmt.Sql()
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	typ := reflect.TypeFor[catalogRow]()
	found, err := scanRows(rows, typ, modelFields(typ))
	if err != nil {
		return err
	}
	c := &catalog{columns: map[[3]string]columnInfo{}, byTable: map[[2]string]columnInfo{}}
	for _, v := range found {
		row := v.Interface().(catalogRow)
		info := columnInfo{
			json:      row.UDT == "json" || row.UDT == "jsonb",
			generated: row.Generated == "ALWAYS" || (row.IdentityGeneration != nil && *row.IdentityGeneration == "ALWAYS"),
			defaulted: row.ColumnDefault != nil || row.Identity == "YES",
		}
		if row.ColumnDefault != nil {
			info.defaultExpr = *row.ColumnDefault
		}
		c.columns[[3]string{row.Schema, row.Table, row.Column}] = info
		c.byTable[[2]string{row.Table, row.Column}] = info
	}
	s.catalog = c
	return nil
}

// catalogInfo is what the catalog says of c, or nothing when it could
// not be read.
func (s *Site) catalogInfo(p *modelPage, c *column) columnInfo {
	s.catalogMu.Lock()
	cat := s.catalog
	s.catalogMu.Unlock()
	if cat == nil {
		return columnInfo{}
	}
	if schema := p.table.SchemaName(); schema != "" {
		return cat.columns[[3]string{schema, p.table.TableName(), c.name}]
	}
	return cat.byTable[[2]string{p.table.TableName(), c.name}]
}

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gateway/config"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

const (
	postgresCurrentVersion = 1
)

type PostgresStore struct {
	conf config.Store
	db   *sqlx.DB
}

type Object struct {
	ID         int64
	AccountID  int64 `db:"account_id"`
	Collection string
	Object     string
}

func (s *PostgresStore) Migrate() error {
	var currentVersion int64
	err := s.db.Get(&currentVersion, `SELECT version FROM schema LIMIT 1`)
	migrate := s.conf.Migrate
	if err != nil {
		tx := s.db.MustBegin()
		tx.MustExec(`
      CREATE TABLE IF NOT EXISTS schema (
        version integer
      );
    `)
		tx.MustExec(`INSERT INTO schema VALUES (0);`)
		err := tx.Commit()
		if err != nil {
			return err
		}

		migrate = true
	}

	if currentVersion == postgresCurrentVersion {
		return nil
	}

	if !migrate {
		return errors.New("The store is not up to date. Please migrate by invoking with the -store-migrate flag.")
	}

	if currentVersion < 1 {
		tx := s.db.MustBegin()
		tx.MustExec(`
      CREATE TABLE IF NOT EXISTS "objects" (
        "id" SERIAL PRIMARY KEY,
        "account_id" INTEGER NOT NULL,
        "collection" TEXT NOT NULL,
        "object" JSON NOT NULL
      );
    `)
		tx.MustExec(`
      CREATE INDEX idx_objects_account_id ON objects USING btree(account_id);
      CREATE INDEX idx_objects_collection ON objects USING btree(collection);
      ANALYZE;
    `)
		tx.MustExec(`UPDATE schema SET version = 1;`)
		err := tx.Commit()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *PostgresStore) SelectByID(accountID int64, collection string, id uint64) (interface{}, error) {
	object := Object{}
	err := s.db.Get(&object, "SELECT id, account_id, collection, object FROM objects WHERE id = $1 AND account_id = $2 AND collection = $3;",
		id, accountID, collection)
	if err != nil {
		return nil, err
	}
	var result interface{}
	err = json.Unmarshal([]byte(object.Object), &result)
	if err != nil {
		return nil, err
	}
	result.(map[string]interface{})["$id"] = object.ID
	return result, nil
}

func (s *PostgresStore) UpdateByID(accountID int64, collection string, id uint64, object interface{}) (interface{}, error) {
	delete(object.(map[string]interface{}), "$id")
	value, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Queryx("UPDATE objects SET object = $1 WHERE id = $2 AND account_id = $3 AND collection = $4;",
		string(value), id, accountID, collection)
	if err != nil {
		return nil, err
	}
	object.(map[string]interface{})["$id"] = id
	return object, nil
}

func (s *PostgresStore) DeleteByID(accountID int64, collection string, id uint64) (interface{}, error) {
	rows, err := s.db.Queryx("DELETE FROM objects WHERE id = $1 AND account_id = $2 AND collection = $3 RETURNING *;", id, accountID, collection)
	if err != nil {
		return nil, err
	}
	var result interface{}
	if rows.Next() {
		var o Object
		err = rows.StructScan(&o)
		if err != nil {
			return nil, err
		}
		err := json.Unmarshal([]byte(o.Object), &result)
		if err != nil {
			return nil, err
		}
		result.(map[string]interface{})["$id"] = o.ID
	}

	return result, nil
}

func (s *PostgresStore) Insert(accountID int64, collection string, object interface{}) (results []interface{}, err error) {
	tx, err := s.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	stmt, err := tx.Preparex(`INSERT into objects (account_id, collection, object) VALUES ($1, $2, $3) RETURNING "id";`)
	if err != nil {
		return nil, err
	}
	add := func(object interface{}) error {
		delete(object.(map[string]interface{}), "$id")
		value, err := json.Marshal(object)
		if err != nil {
			return err
		}
		var id int64
		err = stmt.Get(&id, accountID, collection, string(value))
		if err != nil {
			return err
		}
		object.(map[string]interface{})["$id"] = id
		return nil
	}
	if objects, valid := object.([]interface{}); valid {
		for _, object := range objects {
			err := add(object)
			if err != nil {
				return nil, err
			}
		}
		results = objects
	} else {
		err := add(object)
		if err != nil {
			return nil, err
		}
		results = []interface{}{object}
	}
	return results, nil
}

func (s *PostgresStore) Delete(accountID int64, collection string, query string, params ...interface{}) (results []interface{}, err error) {
	tx, err := s.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	results, err = s._Select(tx, accountID, collection, query, params...)
	if err != nil {
		return nil, err
	}
	stmt, err := tx.Preparex("DELETE FROM objects WHERE id = $1 AND account_id = $2 AND collection = $3;")
	if err != nil {
		return nil, err
	}
	for _, object := range results {
		id := object.(map[string]interface{})["$id"].(int64)
		_, err := stmt.Queryx(id, accountID, collection)
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *PostgresStore) _Select(tx *sqlx.Tx, accountID int64, collection string, query string, params ...interface{}) ([]interface{}, error) {
	jql := &JQL{Buffer: query}
	jql.Init()
	if err := jql.Parse(); err != nil {
		return nil, err
	}
	ast, buffer := jql.tokenTree.AST(), []rune(jql.Buffer)
	query, length := pgProcess(ast, &Context{buffer, nil, params}).s, len(params)
	params = append(params, accountID, collection)
	query = fmt.Sprintf("SELECT id, account_id, collection, object FROM objects WHERE account_id = $%v AND collection = $%v AND %v;",
		length+1, length+2, query)

	rows, err := tx.Queryx(query, params...)
	if err != nil {
		return nil, err
	}

	var results []interface{}
	for rows.Next() {
		var o Object
		err = rows.StructScan(&o)
		if err != nil {
			return nil, err
		}
		var _json interface{}
		err = json.Unmarshal([]byte(o.Object), &_json)
		if err != nil {
			return nil, err
		}
		_json.(map[string]interface{})["$id"] = o.ID
		results = append(results, _json)
	}

	return results, nil
}

func (s *PostgresStore) Select(accountID int64, collection string, query string, params ...interface{}) ([]interface{}, error) {
	tx, err := s.db.Beginx()
	if err != nil {
		return nil, err
	}
	results, err := s._Select(tx, accountID, collection, query, params...)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *PostgresStore) Shutdown() {
	if s.db != nil {
		s.db.Close()
	}
}

type Query struct {
	s      string
	errors []error
}

func pgProcess(node *node32, context *Context) (q Query) {
	for node != nil {
		switch node.pegRule {
		case rulee:
			return pgProcess(node.up, context)
		case rulee1:
			x := pgProcessRulee1(node.up, context)
			q.s += "( " + x.s + " )"
			q.errors = append(q.errors, x.errors...)
		case ruleorder:
			x := pgProcessOrder(node.up, context)
			q.s += " " + x.s
		case rulelimit:
			x := pgProcessLimit(node.up, context)
			q.s += " " + x.s
		case ruleoffset:
			x := pgProcessOffset(node.up, context)
			q.s += " " + x.s
		}
		node = node.next
	}
	return
}

func pgProcessOrder(node *node32, context *Context) (q Query) {
	for node != nil {
		switch node.pegRule {
		case rulepath:
			path := pgProcessPath(node.up, context)
			q.s = "ORDER BY " + path.s
		case rulecast:
			cast := pgProcessCast(node.up, context)
			q.s = "ORDER BY " + cast.s
		case ruleasc:
			q.s += " ASC"
		case ruledesc:
			q.s += " DESC"
		}
		node = node.next
	}
	return
}

func pgProcessCast(node *node32, context *Context) (q Query) {
	for node != nil {
		if node.pegRule == rulepath {
			path := pgProcessPath(node.up, context)
			q.s = "CAST( " + path.s + " as numeric )"
		}
		node = node.next
	}
	return
}

func pgProcessPath(node *node32, context *Context) (q Query) {
	segments := []string{}
	for node != nil {
		if node.pegRule == ruleword {
			segments = append(segments, string(context.buffer[node.begin:node.end]))
		}
		node = node.next
	}
	last := len(segments) - 1
	q.s = "object"
	for _, segment := range segments[:last] {
		q.s += "->'" + segment + "'"
	}
	q.s += "->>'" + segments[last] + "'"
	return
}

func pgProcessLimit(node *node32, context *Context) (q Query) {
	for node != nil {
		if node.pegRule == rulevalue1 {
			q.s += "LIMIT " + strings.TrimSpace(string(context.buffer[node.begin:node.end]))
		}
		node = node.next
	}
	return
}

func pgProcessOffset(node *node32, context *Context) (q Query) {
	for node != nil {
		if node.pegRule == rulevalue1 {
			q.s += "OFFSET " + strings.TrimSpace(string(context.buffer[node.begin:node.end]))
		}
		node = node.next
	}
	return
}

func pgProcessRulee1(node *node32, context *Context) (q Query) {
	or := ""
	for node != nil {
		if node.pegRule == rulee2 {
			x := pgProcessRulee2(node.up, context)
			q.s += or + x.s
			q.errors = append(q.errors, x.errors...)
			or = " OR "
		}
		node = node.next
	}
	return
}

func pgProcessRulee2(node *node32, context *Context) (q Query) {
	and := ""
	for node != nil {
		if node.pegRule == rulee3 {
			x := pgProcessRulee3(node.up, context)
			q.s += and + x.s
			q.errors = append(q.errors, x.errors...)
			and = " AND "
		}
		node = node.next
	}
	return
}

func pgProcessRulee3(node *node32, context *Context) (q Query) {
	if node.pegRule == ruleexpression {
		return pgProcessExpression(node.up, context)
	}
	x := pgProcess(node.next.up, context)
	q.s = "(" + x.s + ")"
	q.errors = x.errors
	return
}

func pgProcessExpression(node *node32, context *Context) (q Query) {
	if node.pegRule == ruleboolean {
		q.s = string(context.buffer[node.begin:node.end])
		return
	}

	path, segments := node.up, []string{}
	for path != nil {
		if path.pegRule == ruleword {
			segments = append(segments, string(context.buffer[path.begin:path.end]))
		}
		path = path.next
	}
	q.s = "object"
	last := len(segments) - 1
	for _, segment := range segments[:last] {
		q.s += "->'" + segment + "'"
	}
	q.s += "->>'" + segments[last] + "'"

	node = node.next
	op := strings.TrimSpace(string(context.buffer[node.begin:node.end]))
	node = node.next.up
	switch node.pegRule {
	case ruleplaceholder:
		placeholder, err := strconv.Atoi(string(context.buffer[node.begin+1 : node.end]))
		if err != nil {
			q.errors = append(q.errors, err)
			return
		}

		if placeholder > len(context.param) {
			q.errors = append(q.errors, errors.New("placholder to large"))
			return
		}
		switch param := context.param[placeholder-1].(type) {
		case string:
			q.s = fmt.Sprintf("%v %v '%v'", q.s, op, param)
		case float64:
			q.s = fmt.Sprintf("CAST(%v as FLOAT) %v %v", q.s, op, param)
		case int:
			q.s = fmt.Sprintf("CAST(%v as INTEGER) %v %v", q.s, op, param)
		case bool:
			q.s = fmt.Sprintf("CAST(%v as BOOLEAN) %v %v", q.s, op, param)
		default:
			switch op {
			case "=":
				q.s = fmt.Sprintf("%v IS NULL", q.s)
			case "!=":
				q.s = fmt.Sprintf("%v IS NOT NULL", q.s)
			}
		}
	case rulestring:
		param := string(context.buffer[node.begin+1 : node.end-1])
		q.s = fmt.Sprintf("%v %v '%v'", q.s, op, param)
	case rulenumber:
		param := string(context.buffer[node.begin:node.end])
		if strings.Contains(param, ".") {
			q.s = fmt.Sprintf("CAST(%v as FLOAT) %v %v", q.s, op, param)
		} else {
			q.s = fmt.Sprintf("CAST(%v as INTEGER) %v %v", q.s, op, param)
		}
	case ruleboolean:
		param := string(context.buffer[node.begin:node.end])
		q.s = fmt.Sprintf("CAST(%v as BOOLEAN) %v %v", q.s, op, param)
	case rulenull:
		switch op {
		case "=":
			q.s = fmt.Sprintf("%v IS NULL", q.s)
		case "!=":
			q.s = fmt.Sprintf("%v IS NOT NULL", q.s)
		}
	}
	return
}

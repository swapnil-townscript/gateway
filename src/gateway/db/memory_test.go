package db

import (
	"reflect"
	"testing"

	"gateway/model"
)

type testModel struct {
	id   int64
	name string `index:"true"`
}

func (t testModel) ID() interface{} {
	return t.id
}

func (t testModel) CollectionName() string {
	return "test_models"
}

type testModel2 struct {
	name string
}

func (t testModel2) ID() interface{} {
	return t.name
}

func (t testModel2) CollectionName() string {
	return "test_models2"
}

var (
	foo = testModel{id: 1, name: "foo"}
	bar = testModel{id: 2, name: "bar"}

	foo2 = testModel2{name: "foo"}
	bar2 = testModel2{name: "bar"}
)

func TestSubMapInitial(t *testing.T) {
	db := NewMemoryStore()
	if len(db.storage) != 0 {
		t.Error("Expected storage to be empty initially")
	}
}

func TestSubMapPerType(t *testing.T) {
	db := NewMemoryStore()
	db.Insert(foo)
	db.Insert(bar)
	db.Insert(foo2)
	db.Insert(bar2)

	testModelSubMapsEqual := reflect.DeepEqual(db.subMap(foo), db.subMap(bar))
	testModel2SubMapsEqual := reflect.DeepEqual(db.subMap(foo2), db.subMap(bar2))
	disparateModelsEqual := reflect.DeepEqual(db.subMap(foo), db.subMap(foo2))

	if !testModelSubMapsEqual || !testModel2SubMapsEqual {
		t.Error("Expected storage to use same subMap for same types")
	}
	if disparateModelsEqual {
		t.Error("Expected storage to use different subMaps for different types")
	}
}

func TestList(t *testing.T) {
	db := NewMemoryStore()
	list, err := db.List(testModel{})
	if err != nil {
		t.Error("Error getting list")
	}

	if len(list) != 0 {
		t.Error("Expected list to have 0 items to start")
	}

	db.Insert(foo)
	db.Insert(bar)

	list, err = db.List(testModel{})
	if err != nil {
		t.Error("Error getting list")
	}
	if len(list) != 2 {
		t.Error("Expected list to have 2 items")
	}
	if !instanceInList(foo, list) {
		t.Error("Expected foo to be in the list")
	}
	if !instanceInList(bar, list) {
		t.Error("Expected bar to be in the list")
	}
}

func TestInsert(t *testing.T) {
	db := NewMemoryStore()
	submap := db.subMap(&foo)
	count := len(submap)
	err := db.Insert(foo)
	if err != nil {
		t.Error("Expected to insert successfully")
	}
	if len(submap) != count+1 {
		t.Error("Expected Insert() to add one to the submap")
	}
	err = db.Insert(foo)
	if err == nil {
		t.Error("Expected duplicate insert to error")
	}
}

func TestInsertIndexed(t *testing.T) {
	db := NewMemoryStore()
	db.Insert(foo)
	submap := db.subMapForFieldName(foo, "name")
	if len(submap) != 1 {
		t.Error("Expected Insert() to add one to the indexed submap")
	}
}

func TestGet(t *testing.T) {
	db := NewMemoryStore()
	_, err := db.Get(testModel{}, 1)
	if err == nil {
		t.Error("Expected Get to return error when instance not present")
	}
	db.Insert(foo)
	instance, err := db.Get(testModel{}, int64(1))
	if err != nil {
		t.Error("Expected Get to not return error when instance is present")
	}
	if instance != foo {
		t.Error("Expected Get to return instance requested")
	}
}

func TestFind(t *testing.T) {
	db := NewMemoryStore()
	_, err := db.Find(testModel{}, "name", "foo")
	if err == nil {
		t.Error("Expected Find to return error when instance not present")
	}
	db.Insert(foo)
	instance, err := db.Find(testModel{}, "name", "foo")
	if err != nil {
		t.Error("Expected Find to not return error when instance is present")
	}
	if instance != foo {
		t.Error("Expected Find to return instance requested")
	}
}

func TestUpdate(t *testing.T) {
	baz := foo

	db := NewMemoryStore()
	if err := db.Update(baz); err == nil {
		t.Error("Expected Update to return error when instance not present")
	}
	db.Insert(baz)
	baz.name = "fii"
	if err := db.Update(baz); err != nil {
		t.Error("Expected Update to not return error when instance is present")
	}
	fetched, _ := db.Get(testModel{}, int64(1))
	if fetched.(testModel).name != "fii" {
		t.Error("Expected Update to update the data in storage")
	}
}

func TestUpdateIndexed(t *testing.T) {
	baz := foo

	db := NewMemoryStore()
	db.Insert(baz)
	baz.name = "fii"
	db.Update(baz)
	_, err := db.Find(testModel{}, "name", "foo")
	if err == nil {
		t.Error("Expected Update to remove old index")
	}
	instance, err := db.Find(testModel{}, "name", "fii")
	if err != nil {
		t.Error("Expected Update to create new index")
	}
	if instance != baz {
		t.Error("Expected Find to return instance requested")
	}
}

func TestDelete(t *testing.T) {
	db := NewMemoryStore()
	if err := db.Delete(foo, int64(1)); err == nil {
		t.Error("Expected Delete to return error when instance not present")
	}
	db.Insert(foo)
	if err := db.Delete(foo, int64(1)); err != nil {
		t.Error("Expected Delete to not return error when instance is present")
	}
	if _, err := db.Get(testModel{}, int64(1)); err == nil {
		t.Error("Expected Delete to remove the instance from storage")
	}
}

func TestDeleteIndexed(t *testing.T) {
	db := NewMemoryStore()
	db.Insert(foo)
	db.Delete(foo, int64(1))
	_, err := db.Find(testModel{}, "name", "foo")
	if err == nil {
		t.Error("Expected Delete to remove old index")
	}
}

func instanceInList(a model.Model, list []interface{}) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
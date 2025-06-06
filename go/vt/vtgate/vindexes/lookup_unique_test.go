/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vindexes

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/key"
	querypb "vitess.io/vitess/go/vt/proto/query"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

func TestLookupUniqueNew(t *testing.T) {
	l := createLookup(t, "lookup_unique", false)
	if want, got := l.(*LookupUnique).writeOnly, false; got != want {
		t.Errorf("Create(lookup, false): %v, want %v", got, want)
	}

	vindex, err := CreateVindex("lookup_unique", "lookup_unique", map[string]string{
		"table":      "t",
		"from":       "fromc",
		"to":         "toc",
		"write_only": "true",
	})
	require.NoError(t, err)
	require.Empty(t, vindex.(ParamValidating).UnknownParams())

	l = vindex.(SingleColumn)
	if want, got := l.(*LookupUnique).writeOnly, true; got != want {
		t.Errorf("Create(lookup, false): %v, want %v", got, want)
	}

	_, err = CreateVindex("lookup_unique", "lookup_unique", map[string]string{
		"table":      "t",
		"from":       "fromc",
		"to":         "toc",
		"write_only": "invalid",
	})
	want := "write_only value must be 'true' or 'false': 'invalid'"
	if err == nil || err.Error() != want {
		t.Errorf("Create(bad_scatter): %v, want %s", err, want)
	}
}

func TestLookupUniqueMap(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", false)
	vc := &vcursor{numRows: 1}

	got, err := lookupUnique.Map(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1), sqltypes.NewInt64(2)})
	require.NoError(t, err)
	want := []key.ShardDestination{
		key.DestinationKeyspaceID([]byte("1")),
		key.DestinationNone{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Map(): %+v, want %+v", got, want)
	}

	vc.numRows = 0
	got, err = lookupUnique.Map(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1), sqltypes.NewInt64(2)})
	require.NoError(t, err)
	want = []key.ShardDestination{
		key.DestinationNone{},
		key.DestinationNone{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Map(): %#v, want %+v", got, want)
	}

	vc.numRows = 2
	_, err = lookupUnique.Map(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1), sqltypes.NewInt64(2)})
	wantErr := "Lookup.Map: unexpected multiple results from vindex t: INT64(1)"
	if err == nil || err.Error() != wantErr {
		t.Errorf("lookupUnique(query fail) err: %v, want %s", err, wantErr)
	}

	// Test query fail.
	vc.mustFail = true
	_, err = lookupUnique.Map(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1)})
	wantErr = "lookup.Map: execute failed"
	if err == nil || err.Error() != wantErr {
		t.Errorf("lookupUnique(query fail) err: %v, want %s", err, wantErr)
	}
	vc.mustFail = false
}

func TestLookupUniqueMapWriteOnly(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", true)
	vc := &vcursor{numRows: 0}

	got, err := lookupUnique.Map(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1), sqltypes.NewInt64(2)})
	require.NoError(t, err)
	want := []key.ShardDestination{
		key.DestinationKeyRange{
			KeyRange: &topodatapb.KeyRange{},
		},
		key.DestinationKeyRange{
			KeyRange: &topodatapb.KeyRange{},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Map(): %#v, want %+v", got, want)
	}
}

func TestLookupUniqueVerify(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", false)
	vc := &vcursor{numRows: 1}

	_, err := lookupUnique.Verify(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1)}, [][]byte{[]byte("test")})
	require.NoError(t, err)
	if got, want := len(vc.queries), 1; got != want {
		t.Errorf("vc.queries length: %v, want %v", got, want)
	}
}

func TestLookupUniqueVerifyWriteOnly(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", true)
	vc := &vcursor{numRows: 0}

	got, err := lookupUnique.Verify(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1)}, [][]byte{[]byte("test")})
	require.NoError(t, err)
	want := []bool{true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lookupUnique.Verify: %v, want %v", got, want)
	}
	if got, want := len(vc.queries), 0; got != want {
		t.Errorf("vc.queries length: %v, want %v", got, want)
	}
}

func TestLookupUniqueCreate(t *testing.T) {
	lookupUnique, err := CreateVindex("lookup_unique", "lookup_unique", map[string]string{
		"table":      "t",
		"from":       "from",
		"to":         "toc",
		"autocommit": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	require.Empty(t, lookupUnique.(ParamValidating).UnknownParams())
	vc := &vcursor{}

	err = lookupUnique.(Lookup).Create(context.Background(), vc, [][]sqltypes.Value{{sqltypes.NewInt64(1)}}, [][]byte{[]byte("test")}, false /* ignoreMode */)
	require.NoError(t, err)

	wantqueries := []*querypb.BoundQuery{{
		Sql: "insert into t(from, toc) values(:from_0, :toc_0)",
		BindVariables: map[string]*querypb.BindVariable{
			"from_0": sqltypes.Int64BindVariable(1),
			"toc_0":  sqltypes.BytesBindVariable([]byte("test")),
		},
	}}
	if !reflect.DeepEqual(vc.queries, wantqueries) {
		t.Errorf("lookup.Create queries:\n%v, want\n%v", vc.queries, wantqueries)
	}

	if got, want := vc.autocommits, 1; got != want {
		t.Errorf("Create(autocommit) count: %d, want %d", got, want)
	}
}

func TestLookupUniqueCreateAutocommit(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", false)
	vc := &vcursor{}

	err := lookupUnique.(Lookup).Create(context.Background(), vc, [][]sqltypes.Value{{sqltypes.NewInt64(1)}}, [][]byte{[]byte("test")}, false /* ignoreMode */)
	require.NoError(t, err)
	if got, want := len(vc.queries), 1; got != want {
		t.Errorf("vc.queries length: %v, want %v", got, want)
	}
}

func TestLookupUniqueDelete(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", false)
	vc := &vcursor{}

	err := lookupUnique.(Lookup).Delete(context.Background(), vc, [][]sqltypes.Value{{sqltypes.NewInt64(1)}}, []byte("test"))
	require.NoError(t, err)
	if got, want := len(vc.queries), 1; got != want {
		t.Errorf("vc.queries length: %v, want %v", got, want)
	}
}

func TestLookupUniqueUpdate(t *testing.T) {
	lookupUnique := createLookup(t, "lookup_unique", false)
	vc := &vcursor{}

	err := lookupUnique.(Lookup).Update(context.Background(), vc, []sqltypes.Value{sqltypes.NewInt64(1)}, []byte("test"), []sqltypes.Value{sqltypes.NewInt64(2)})
	require.NoError(t, err)
	if got, want := len(vc.queries), 2; got != want {
		t.Errorf("vc.queries length: %v, want %v", got, want)
	}
}

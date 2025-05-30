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

package vitessdriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/vtgate/grpcvtgateservice"
)

var testAddress string

// TestMain tests the Vitess Go SQL driver.
//
// Note that the queries used in the test are not valid SQL queries and don't
// have to be. The main point here is to test the interactions against a
// vtgate implementation (here: fakeVTGateService from fakeserver_test.go).
func TestMain(m *testing.M) {
	service := CreateFakeServer()

	// listen on a random port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("Cannot listen: %v", err))
	}

	// Create a gRPC server and listen on the port.
	server := grpc.NewServer()
	grpcvtgateservice.RegisterForTest(server, service)
	go server.Serve(listener)

	testAddress = listener.Addr().String()
	os.Exit(m.Run())
}

func TestOpen(t *testing.T) {
	locationPST, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}

	testcases := []struct {
		desc    string
		connStr string
		conn    *conn
	}{
		{
			desc:    "Open()",
			connStr: fmt.Sprintf(`{"address": "%s", "target": "@replica", "timeout": %d}`, testAddress, int64(30*time.Second)),
			conn: &conn{
				cfg: Configuration{
					Protocol:   "grpc",
					DriverName: "vitess",
					Target:     "@replica",
				},
				convert: &converter{
					location: time.UTC,
				},
			},
		},
		{
			desc:    "Open() (defaults omitted)",
			connStr: fmt.Sprintf(`{"address": "%s", "timeout": %d}`, testAddress, int64(30*time.Second)),
			conn: &conn{
				cfg: Configuration{
					Protocol:   "grpc",
					DriverName: "vitess",
				},
				convert: &converter{
					location: time.UTC,
				},
			},
		},
		{
			desc:    "Open() with keyspace",
			connStr: fmt.Sprintf(`{"protocol": "grpc", "address": "%s", "target": "ks:0@replica", "timeout": %d}`, testAddress, int64(30*time.Second)),
			conn: &conn{
				cfg: Configuration{
					Protocol:   "grpc",
					DriverName: "vitess",
					Target:     "ks:0@replica",
				},
				convert: &converter{
					location: time.UTC,
				},
			},
		},
		{
			desc: "Open() with custom timezone",
			connStr: fmt.Sprintf(
				`{"address": "%s", "timeout": %d, "defaultlocation": "America/Los_Angeles"}`,
				testAddress, int64(30*time.Second)),
			conn: &conn{
				cfg: Configuration{
					Protocol:        "grpc",
					DriverName:      "vitess",
					DefaultLocation: "America/Los_Angeles",
				},
				convert: &converter{
					location: locationPST,
				},
			},
		},
	}

	for _, tc := range testcases {
		c, err := drv{}.Open(tc.connStr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()

		wantc := tc.conn
		newc := *(c.(*conn))
		newc.cfg.Address = ""
		newc.conn = nil
		newc.session = nil
		if !reflect.DeepEqual(&newc, wantc) {
			t.Errorf("%v: conn:\n%+v, want\n%+v", tc.desc, &newc, wantc)
		}
	}
}

func TestOpen_UnregisteredProtocol(t *testing.T) {
	_, err := drv{}.Open(`{"protocol": "none"}`)
	want := "no dialer registered for VTGate protocol none"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err: %v, want %s", err, want)
	}
}

func TestOpen_InvalidJson(t *testing.T) {
	_, err := drv{}.Open(`{`)
	want := "unexpected end of JSON input"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err: %v, want %s", err, want)
	}
}

func TestBeginIsolation(t *testing.T) {
	db, err := Open(testAddress, "@primary")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	want := errIsolationUnsupported.Error()
	if err == nil || err.Error() != want {
		t.Errorf("Begin: %v, want %s", err, want)
	}
}

func TestExec(t *testing.T) {
	db, err := Open(testAddress, "@rdonly")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s, err := db.Prepare("request")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	r, err := s.Exec(int64(0))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r.LastInsertId(); v != 72 {
		t.Errorf("insert id: %d, want 72", v)
	}
	if v, _ := r.RowsAffected(); v != 123 {
		t.Errorf("rows affected: %d, want 123", v)
	}

	s2, err := db.Prepare("none")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	_, err = s2.Exec()
	want := "no match for: none"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err: %v, does not contain %s", err, want)
	}
}

func TestConfigurationToJSON(t *testing.T) {
	config := Configuration{
		Protocol:        "some-invalid-protocol",
		Target:          "ks2",
		Streaming:       true,
		DefaultLocation: "Local",
	}
	want := `{"Protocol":"some-invalid-protocol","Address":"","Target":"ks2","Streaming":true,"DefaultLocation":"Local","SessionToken":""}`

	json, err := config.toJSON()
	if err != nil {
		t.Fatal(err)
	}
	if json != want {
		t.Errorf("Configuration.JSON(): got: %v want: %v", json, want)
	}
}

func TestExecStreamingNotAllowed(t *testing.T) {
	db, err := OpenForStreaming(testAddress, "@rdonly")
	if err != nil {
		t.Fatal(err)
	}

	s, err := db.Prepare("request")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.Exec(int64(0))
	want := "Exec not allowed for streaming connections"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err: %v, does not contain %s", err, want)
	}
}

func TestQuery(t *testing.T) {
	testcases := []struct {
		desc        string
		config      Configuration
		requestName string
	}{
		{
			desc: "non-streaming, vtgate",
			config: Configuration{
				Protocol: "grpc",
				Address:  testAddress,
				Target:   "@rdonly",
			},
			requestName: "request",
		},
		{
			desc: "streaming, vtgate",
			config: Configuration{
				Protocol:  "grpc",
				Address:   testAddress,
				Target:    "@rdonly",
				Streaming: true,
			},
			requestName: "request",
		},
	}

	for _, tc := range testcases {
		db, err := OpenWithConfiguration(tc.config)
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer db.Close()

		s, err := db.Prepare(tc.requestName)
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer s.Close()

		r, err := s.Query(int64(0))
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer r.Close()
		cols, err := r.Columns()
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		wantCols := []string{
			"field1",
			"field2",
		}
		if !reflect.DeepEqual(cols, wantCols) {
			t.Errorf("%v: cols: %v, want %v", tc.desc, cols, wantCols)
		}
		count := 0
		wantValues := []struct {
			field1 int16
			field2 string
		}{{1, "value1"}, {2, "value2"}}
		for r.Next() {
			var field1 int16
			var field2 string
			err := r.Scan(&field1, &field2)
			if err != nil {
				t.Errorf("%v: %v", tc.desc, err)
			}
			if want := wantValues[count].field1; field1 != want {
				t.Errorf("%v: wrong value for field1: got: %v want: %v", tc.desc, field1, want)
			}
			if want := wantValues[count].field2; field2 != want {
				t.Errorf("%v: wrong value for field2: got: %v want: %v", tc.desc, field2, want)
			}
			count++
		}
		if count != len(wantValues) {
			t.Errorf("%v: count: %d, want %d", tc.desc, count, len(wantValues))
		}

		s2, err := db.Prepare("none")
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer s2.Close()

		rows, err := s2.Query()
		want := "no match for: none"
		if tc.config.Streaming && err == nil {
			defer rows.Close()
			// gRPC requires to consume the stream first before the error becomes visible.
			if rows.Next() {
				t.Errorf("%v: query should not have returned anything but did.", tc.desc)
			}
			err = rows.Err()
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err: %v, does not contain %s", tc.desc, err, want)
		}
	}
}

func TestBindVars(t *testing.T) {
	testcases := []struct {
		desc   string
		in     []driver.NamedValue
		out    map[string]*querypb.BindVariable
		outErr string
	}{{
		desc: "all names",
		in: []driver.NamedValue{{
			Name:  "n1",
			Value: int64(0),
		}, {
			Name:  "n2",
			Value: "abcd",
		}},
		out: map[string]*querypb.BindVariable{
			"n1": sqltypes.Int64BindVariable(0),
			"n2": sqltypes.StringBindVariable("abcd"),
		},
	}, {
		desc: "prefixed names",
		in: []driver.NamedValue{{
			Name:  ":n1",
			Value: int64(0),
		}, {
			Name:  "@n2",
			Value: "abcd",
		}},
		out: map[string]*querypb.BindVariable{
			"n1": sqltypes.Int64BindVariable(0),
			"n2": sqltypes.StringBindVariable("abcd"),
		},
	}, {
		desc: "all positional",
		in: []driver.NamedValue{{
			Ordinal: 1,
			Value:   int64(0),
		}, {
			Ordinal: 2,
			Value:   "abcd",
		}},
		out: map[string]*querypb.BindVariable{
			"v1": sqltypes.Int64BindVariable(0),
			"v2": sqltypes.StringBindVariable("abcd"),
		},
	}, {
		desc: "name, then position",
		in: []driver.NamedValue{{
			Name:  "n1",
			Value: int64(0),
		}, {
			Ordinal: 2,
			Value:   "abcd",
		}},
		outErr: errNoIntermixing.Error(),
	}, {
		desc: "position, then name",
		in: []driver.NamedValue{{
			Ordinal: 1,
			Value:   int64(0),
		}, {
			Name:  "n2",
			Value: "abcd",
		}},
		outErr: errNoIntermixing.Error(),
	}}

	converter := &converter{}

	for _, tc := range testcases {
		t.Run(tc.desc, func(t *testing.T) {
			bv, err := converter.bindVarsFromNamedValues(tc.in)
			if tc.outErr != "" {
				assert.EqualError(t, err, tc.outErr)
			} else {
				if !reflect.DeepEqual(bv, tc.out) {
					t.Errorf("%s: %v, want %v", tc.desc, bv, tc.out)
				}
			}
		})
	}
}

func TestDatetimeQuery(t *testing.T) {
	testcases := []struct {
		desc        string
		config      Configuration
		requestName string
	}{
		{
			desc: "datetime & date, vtgate",
			config: Configuration{
				Protocol: "grpc",
				Address:  testAddress,
				Target:   "@rdonly",
			},
			requestName: "requestDates",
		},
		{
			desc: "datetime & date (local timezone), vtgate",
			config: Configuration{
				Protocol:        "grpc",
				Address:         testAddress,
				Target:          "@rdonly",
				DefaultLocation: "Local",
			},
			requestName: "requestDates",
		},
		{
			desc: "datetime & date, streaming, vtgate",
			config: Configuration{
				Protocol:  "grpc",
				Address:   testAddress,
				Target:    "@rdonly",
				Streaming: true,
			},
			requestName: "requestDates",
		},
	}

	for _, tc := range testcases {
		db, err := OpenWithConfiguration(tc.config)
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer db.Close()

		s, err := db.Prepare(tc.requestName)
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer s.Close()

		r, err := s.Query(0)
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		defer r.Close()

		cols, err := r.Columns()
		if err != nil {
			t.Errorf("%v: %v", tc.desc, err)
		}
		wantCols := []string{
			"fieldDatetime",
			"fieldDate",
		}
		if !reflect.DeepEqual(cols, wantCols) {
			t.Errorf("%v: cols: %v, want %v", tc.desc, cols, wantCols)
		}

		location := time.UTC
		if tc.config.DefaultLocation != "" {
			location, err = time.LoadLocation(tc.config.DefaultLocation)
			if err != nil {
				t.Errorf("%v: %v", tc.desc, err)
			}
		}

		count := 0
		wantValues := []struct {
			fieldDatetime time.Time
			fieldDate     time.Time
		}{{
			time.Date(2009, 3, 29, 17, 22, 11, 0, location),
			time.Date(2006, 7, 2, 0, 0, 0, 0, location),
		}, {
			time.Time{},
			time.Time{},
		}}

		for r.Next() {
			var fieldDatetime time.Time
			var fieldDate time.Time
			err := r.Scan(&fieldDatetime, &fieldDate)
			if err != nil {
				t.Errorf("%v: %v", tc.desc, err)
			}
			if want := wantValues[count].fieldDatetime; fieldDatetime != want {
				t.Errorf("%v: wrong value for fieldDatetime: got: %v want: %v", tc.desc, fieldDatetime, want)
			}
			if want := wantValues[count].fieldDate; fieldDate != want {
				t.Errorf("%v: wrong value for fieldDate: got: %v want: %v", tc.desc, fieldDate, want)
			}
			count++
		}

		if count != len(wantValues) {
			t.Errorf("%v: count: %d, want %d", tc.desc, count, len(wantValues))
		}
	}
}

func TestTx(t *testing.T) {
	c := Configuration{
		Protocol: "grpc",
		Address:  testAddress,
		Target:   "@primary",
	}

	db, err := OpenWithConfiguration(c)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	s, err := tx.Prepare("txRequest")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Exec(int64(0))
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	// Commit on committed transaction is caught by Golang sql package.
	// We actually don't have to cover this in our code.
	err = tx.Commit()
	if err != sql.ErrTxDone {
		t.Errorf("err: %v, not ErrTxDone", err)
	}

	// Test rollback now.
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	s, err = tx.Prepare("txRequest")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Query(int64(0))
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	// Rollback on rolled back transaction is caught by Golang sql package.
	// We actually don't have to cover this in our code.
	err = tx.Rollback()
	if err != sql.ErrTxDone {
		t.Errorf("err: %v, not ErrTxDone", err)
	}
}

func TestTxExecStreamingNotAllowed(t *testing.T) {
	db, err := OpenForStreaming(testAddress, "@rdonly")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Begin()
	want := "Exec not allowed for streaming connection"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err: %v, does not contain %s", err, want)
	}
}

func TestSessionToken(t *testing.T) {
	c := Configuration{
		Protocol: "grpc",
		Address:  testAddress,
		Target:   "@primary",
	}

	ctx := context.Background()

	db, err := OpenWithConfiguration(c)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	s, err := tx.Prepare("txRequest")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Exec(int64(0))
	if err != nil {
		t.Fatal(err)
	}

	sessionToken, err := SessionTokenFromTx(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}

	distributedTxConfig := Configuration{
		Address:      testAddress,
		Target:       "@primary",
		SessionToken: sessionToken,
	}

	sameTx, sameValidationFunc, err := DistributedTxFromSessionToken(ctx, distributedTxConfig)
	if err != nil {
		t.Fatal(err)
	}

	newS, err := sameTx.Prepare("distributedTxRequest")
	if err != nil {
		t.Fatal(err)
	}

	_, err = newS.Exec(int64(1))
	if err != nil {
		t.Fatal(err)
	}

	err = sameValidationFunc()
	if err != nil {
		t.Fatal(err)
	}

	// enforce that Rollback can't be called on the distributed tx
	noRollbackTx, noRollbackValidationFunc, err := DistributedTxFromSessionToken(ctx, distributedTxConfig)
	if err != nil {
		t.Fatal(err)
	}

	err = noRollbackValidationFunc()
	if err != nil {
		t.Fatal(err)
	}

	err = noRollbackTx.Rollback()
	if err == nil || err.Error() != "calling Rollback from a distributed tx is not allowed" {
		t.Fatal(err)
	}

	// enforce that Commit can't be called on the distributed tx
	noCommitTx, noCommitValidationFunc, err := DistributedTxFromSessionToken(ctx, distributedTxConfig)
	if err != nil {
		t.Fatal(err)
	}

	err = noCommitValidationFunc()
	if err != nil {
		t.Fatal(err)
	}

	err = noCommitTx.Commit()
	if err == nil || err.Error() != "calling Commit from a distributed tx is not allowed" {
		t.Fatal(err)
	}

	// finally commit the original tx
	err = tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
}

// TestStreamExec tests that different kinds of query present in `execMap` can run through streaming api
func TestStreamExec(t *testing.T) {
	db, err := OpenForStreaming(testAddress, "@rdonly")
	require.NoError(t, err)
	defer db.Close()

	for k, v := range createExecMap() {
		t.Run(k, func(t *testing.T) {
			s, err := db.Prepare(k)
			require.NoError(t, err)
			defer s.Close()

			r, err := s.Query(0)
			require.NoError(t, err)
			defer r.Close()

			fields, err := r.Columns()
			require.NoError(t, err)
			require.Equal(t, colList(v.result.Fields), fields)

			for r.Next() {
				require.NoError(t, r.Err())
			}
		})
	}
}

func colList(fields []*querypb.Field) []string {
	if fields == nil {
		return nil
	}
	cols := make([]string, 0, len(fields))
	for _, field := range fields {
		cols = append(cols, field.Name)
	}
	return cols
}

func TestConnSeparateSessions(t *testing.T) {
	c := Configuration{
		Protocol: "grpc",
		Address:  testAddress,
		Target:   "@primary",
	}

	db, err := OpenWithConfiguration(c)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Each new connection starts a fresh session pointed at @primary. When the
	// USE statement is executed, we simulate a change to that individual
	// connection's target string.
	//
	// No connections are returned to the pool during this test and therefore
	// the connection state should not be shared.
	var conns []*sql.Conn
	for i := 0; i < 3; i++ {
		sconn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, sconn)

		targets := []string{targetString(t, sconn)}

		_, err = sconn.ExecContext(ctx, "use @rdonly")
		require.NoError(t, err)

		targets = append(targets, targetString(t, sconn))

		require.Equal(t, []string{"@primary", "@rdonly"}, targets)
	}

	for _, c := range conns {
		require.NoError(t, c.Close())
	}
}

func TestConnReuseSessions(t *testing.T) {
	c := Configuration{
		Protocol: "grpc",
		Address:  testAddress,
		Target:   "@primary",
	}

	db, err := OpenWithConfiguration(c)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Pull an individual connection from the pool and execute a USE, resulting
	// in changing the target string. We return the connection to the pool
	// continuously in this test and verify that we keep pulling the same
	// connection with its target string altered.
	sconn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sconn.ExecContext(ctx, "use @rdonly")
	require.NoError(t, err)
	require.NoError(t, sconn.Close())

	var targets []string
	for i := 0; i < 3; i++ {
		sconn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}

		targets = append(targets, targetString(t, sconn))
		require.NoError(t, sconn.Close())
	}

	require.Equal(t, []string{"@rdonly", "@rdonly", "@rdonly"}, targets)
}

func targetString(t *testing.T, c *sql.Conn) string {
	t.Helper()

	var target string
	require.NoError(t, c.Raw(func(driverConn any) error {
		target = driverConn.(*conn).session.SessionPb().TargetString
		return nil
	}))

	return target
}
